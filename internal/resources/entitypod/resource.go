// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package entitypod implements the fianu_entity_pod Terraform resource — the
// generic escape hatch for attaching a pod to any Fianu entity.
//
// Pods are "living configuration": rows keyed by (entity_id, pod_type, key)
// that hang off an entity and change how it behaves, without minting a new
// entity version. They are not entities themselves — no envelope, no path, no
// version history — so this resource deliberately does not use
// `internal/resources/base`'s envelope machinery.
//
// This resource is intentionally untyped: `value` is a JSON string the user
// authors with `jsonencode({...})`. Pod types are added to the platform far
// faster than a Terraform resource can be shipped per type, so a generic
// resource means new pod types work on day one. Where a pod type is worth real
// HCL — validators, nested blocks, docs — it gets a typed resource on top of
// the same four SDK calls; `fianu_notification` is the first.
//
// Scope: entity-scoped pods only. Tenant-, asset-, and user-scope pods are
// per-human runtime preferences (quiet hours, per-asset mutes) rather than
// declarative infrastructure, and their routes are not in the SDK's generated
// surface.
package entitypod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/fianulabs/core/v2/external/db/pods"
	sdk "github.com/fianulabs/core/v2/external/pkg/sdk/v2"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Compile-time interface checks.
var (
	_ resource.Resource                = (*podResource)(nil)
	_ resource.ResourceWithConfigure   = (*podResource)(nil)
	_ resource.ResourceWithImportState = (*podResource)(nil)
)

// NewResource is the factory the provider package registers.
func NewResource() resource.Resource {
	return &podResource{}
}

type podResource struct {
	client *sdk.Client
}

type podModel struct {
	ID          types.String `tfsdk:"id"`
	EntityUUID  types.String `tfsdk:"entity_uuid"`
	PodType     types.String `tfsdk:"pod_type"`
	Key         types.String `tfsdk:"key"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	Value       types.String `tfsdk:"value"`
}

func (r *podResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_entity_pod"
}

func (r *podResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// The (entity_uuid, pod_type, key) triple IS the pod's server-side
	// primary key. Changing any of them addresses a different row, so each
	// requires replacement — otherwise an update would write a new pod and
	// silently orphan the old one.
	requiresReplace := []planmodifier.String{stringplanmodifier.RequiresReplace()}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches a pod to a Fianu entity. Pods are living configuration — JSON-valued rows keyed by `(entity_id, pod_type, key)` that change how an entity behaves without minting a new entity version.\n\nThis is the generic form: `value` is a JSON string you author with `jsonencode({...})`, so pod types the provider has no typed resource for still work. For notification pods, prefer `fianu_notification`, which validates the same payload and exposes it as real HCL.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Composite resource ID — `<entity_uuid>/<pod_type>/<key>`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"entity_uuid": schema.StringAttribute{
				MarkdownDescription: "UUID of the entity this pod hangs off (e.g., `fianu_gate.deploy.uuid`, `fianu_control.sast.uuid`).",
				Required:            true,
				PlanModifiers:       requiresReplace,
			},
			"pod_type": schema.StringAttribute{
				MarkdownDescription: "The pod type bucket, e.g. `platforms_capabilities_data_exports_gating`, `display`, `llm_context_rule`. See the Fianu Console pod catalog for the types valid on a given entity.",
				Required:            true,
				PlanModifiers:       requiresReplace,
			},
			"key": schema.StringAttribute{
				MarkdownDescription: "Pod key, unique per `(entity, pod_type)`. Many pod types use a structured key to fan out — platform-capability pods use `\"<capability>:<instanceKey>\"`, notification pods use `\"config\"`.",
				Required:            true,
				PlanModifiers:       requiresReplace,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Human-readable name for the pod.",
				Optional:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description.",
				Optional:            true,
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the pod is active. Defaults to `true`.",
				Optional:            true,
			},
			"value": schema.StringAttribute{
				MarkdownDescription: "The pod's JSON payload. Author with `jsonencode({...})`. The shape is defined by `pod_type` and validated server-side on write.",
				Required:            true,
			},
		},
	}
}

func (r *podResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*sdk.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"unexpected provider data",
			fmt.Sprintf("expected *sdk.Client, got %T. This is a provider bug.", req.ProviderData),
		)
		return
	}
	r.client = client
}

func (r *podResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan podModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read confirms the pod still exists and evicts state if it does not.
//
// It deliberately does NOT hydrate `value` back from the server. The server
// canonicalises and reorders JSON on write, so echoing it into state would
// surface a permanent diff against the user's `jsonencode` output on every
// plan. Same rule the entity resources follow for their Detail sections.
func (r *podResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state podModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.GetEntityPod(ctx, state.EntityUUID.ValueString(), state.PodType.ValueString(), state.Key.ValueString())
	if err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch entity pod failed", err.Error())
		return
	}

	state.ID = types.StringValue(FormatID(state.EntityUUID.ValueString(), state.PodType.ValueString(), state.Key.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *podResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan podModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *podResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state podModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteEntityPod(ctx, state.EntityUUID.ValueString(), state.PodType.ValueString(), state.Key.ValueString())
	if err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("delete entity pod failed", err.Error())
	}
}

func (r *podResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	entityUUID, podType, key, err := ParseID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("entity_uuid"), entityUUID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pod_type"), podType)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), key)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// set upserts the pod. SetEntityPod is a PUT — idempotent for the same
// (entity, type, key) — so Create and Update are the same call.
func (r *podResource) set(ctx context.Context, plan *podModel) diag.Diagnostics {
	var diags diag.Diagnostics

	raw := json.RawMessage(plan.Value.ValueString())
	if !json.Valid(raw) {
		diags.AddAttributeError(path.Root("value"), "value is not valid JSON",
			"The pod value must be a JSON document. Author it with jsonencode({...}).")
		return diags
	}

	pod := Build(plan.PodType.ValueString(), plan.Key.ValueString(), raw, plan.Name, plan.Description, plan.Enabled)
	if _, err := r.client.SetEntityPod(ctx, plan.EntityUUID.ValueString(), pod.PodType, pod.Key, pod); err != nil {
		diags.AddError("set entity pod failed", err.Error())
		return diags
	}

	plan.ID = types.StringValue(FormatID(plan.EntityUUID.ValueString(), pod.PodType, pod.Key))
	return diags
}

// Build assembles a pods.Pod from the common HCL fields. Shared with
// fianu_notification, which supplies a typed value instead of a raw string.
func Build(podType, key string, value json.RawMessage, name, description types.String, enabled types.Bool) pods.Pod {
	pod := pods.Pod{
		PodType: podType,
		Key:     key,
		Value:   value,
	}
	if s := optionalString(name); s != nil {
		pod.Name = s
	}
	if s := optionalString(description); s != nil {
		pod.Description = s
	}
	// Absent means enabled: a pod exists because someone authored it.
	on := true
	if !enabled.IsNull() && !enabled.IsUnknown() {
		on = enabled.ValueBool()
	}
	pod.Enabled = &on
	return pod
}

func optionalString(v types.String) *string {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}
	s := v.ValueString()
	return &s
}

// IsNotFound reports whether err is an SDK 404. Used to distinguish
// evict-state-on-gone from any-other-error-is-a-diagnostic; a blanket
// `err != nil` would silently drop resources from state on a transient
// failure.
func IsNotFound(err error) bool {
	var apiErr *sdk.APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// FormatID builds the composite resource ID for a pod.
func FormatID(entityUUID, podType, key string) string {
	return entityUUID + "/" + podType + "/" + key
}

// ParseID splits a composite pod ID. The key may itself contain slashes, so
// only the first two separators are structural.
//
// pod_type is NOT validated against the SDK's PodType enum. The whole point of
// this resource is that a pod type the platform ships before the provider
// bumps its SDK still works — rejecting unknown types on import while the
// schema accepts them on create would be backwards. fianu_notification, which
// does constrain its types, checks that itself in ImportState.
func ParseID(id string) (entityUUID, podType, key string, err error) {
	parts := strings.SplitN(id, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("expected %q, got %q", "<entity_uuid>/<pod_type>/<key>", id)
	}
	return parts[0], parts[1], parts[2], nil
}
