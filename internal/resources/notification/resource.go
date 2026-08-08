// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package notification implements the fianu_notification Terraform resource —
// a notification config pod attached to a control or gate.
//
// It is `fianu_entity_pod` with a typed schema: same four SDK calls, same
// `(entity_id, pod_type, key)` addressing, but the pod value is built from
// real HCL and validated at plan time instead of hand-written JSON.
//
// Which notifications a pod covers is decided by `type` — the pod_type bucket.
// Several emitted notification types share one bucket (attestation
// fail/warn/notfound/error/recovery all land in
// `notification_attestation_failure`), so configuring the bucket configures
// the family. `../core/external/db/variables/notification.go` is the registry.
//
// `rules` reuses `PolicyAssetGroup` — the same asset-matching primitive behind
// `fianu_policy`'s variation criteria and `fianu_gate`'s check matching. One
// authoring surface, one CEL evaluator; see `internal/resources/base/criteria.go`.
//
// Scope: entity-scoped pods only. The tenant-, asset-, and user-scope
// notification pods (channel destinations, integrations, quiet hours, per-asset
// mutes) are per-human runtime preferences rather than declarative
// infrastructure, and their routes are not in the SDK's generated surface.
package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	entity_pods "github.com/fianulabs/core/v2/external/db/types/fianu/entities/pods"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	sdk "github.com/fianulabs/core/v2/external/pkg/sdk/v2"
	pkgvars "github.com/fianulabs/core/v2/external/pkg/variables"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/fianulabs/terraform-provider-fianu/internal/resources/base"
	"github.com/fianulabs/terraform-provider-fianu/internal/resources/entitypod"
)

// Compile-time interface checks.
var (
	_ resource.Resource                = (*notificationResource)(nil)
	_ resource.ResourceWithConfigure   = (*notificationResource)(nil)
	_ resource.ResourceWithImportState = (*notificationResource)(nil)
)

// NewResource is the factory the provider package registers.
func NewResource() resource.Resource {
	return &notificationResource{}
}

type notificationResource struct {
	client *sdk.Client
}

type notificationModel struct {
	ID         types.String `tfsdk:"id"`
	EntityUUID types.String `tfsdk:"entity_uuid"`
	Type       types.String `tfsdk:"type"`
	Key        types.String `tfsdk:"key"`

	Enabled    types.Bool     `tfsdk:"enabled"`
	Muted      types.Bool     `tfsdk:"muted"`
	MutedUntil types.String   `tfsdk:"muted_until"`
	Urgency    types.Int64    `tfsdk:"urgency"`
	Mode       types.String   `tfsdk:"mode"`
	Recipients []types.String `tfsdk:"recipients"`
	Channels   []types.String `tfsdk:"channels"`

	Rules   *base.CriteriaModel    `tfsdk:"rules"`
	Params  *paramsModel           `tfsdk:"params"`
	Filters map[string]filterModel `tfsdk:"filters"`
}

// paramsModel are the type-specific tuning knobs. Every field is optional and
// only meaningful for some buckets — the server applies engine defaults for
// anything left unset, which is why they are all nullable rather than
// defaulted here.
type paramsModel struct {
	LeadWindowDays types.Int64  `tfsdk:"lead_window_days"`
	DurationDays   types.Int64  `tfsdk:"duration_days"`
	WindowDays     types.Int64  `tfsdk:"window_days"`
	MinCount       types.Int64  `tfsdk:"min_count"`
	ThresholdPct   types.Int64  `tfsdk:"threshold_pct"`
	MinAssets      types.Int64  `tfsdk:"min_assets"`
	ControlScope   types.String `tfsdk:"control_scope"`
}

type filterModel struct {
	Enabled types.Bool  `tfsdk:"enabled"`
	Urgency types.Int64 `tfsdk:"urgency"`
}

func (r *notificationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification"
}

func (r *notificationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// The (entity_uuid, type, key) triple is the pod's server-side primary
	// key — changing any of them addresses a different row.
	requiresReplace := []planmodifier.String{stringplanmodifier.RequiresReplace()}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Configures notifications for a Fianu control or gate. Stored as a notification config pod attached to the entity — living configuration that changes how the entity behaves without minting a new entity version.\n\nOne resource configures one notification *bucket*, and several emitted notification types share a bucket: `notification_attestation_failure` covers attestation fail, warn, not-found, error, and recovery. Set `rules` to fire only for a subset of assets — it is the same asset-matching primitive as `fianu_policy` variation criteria and `fianu_gate` check matching.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Composite resource ID — `<entity_uuid>/<type>/<key>`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"entity_uuid": schema.StringAttribute{
				MarkdownDescription: "UUID of the control or gate these notifications are configured on (e.g., `fianu_control.sast.uuid`).",
				Required:            true,
				PlanModifiers:       requiresReplace,
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "The notification bucket to configure. One of: " + podTypeDocList() + ".",
				Required:            true,
				PlanModifiers:       requiresReplace,
				Validators: []validator.String{
					stringvalidator.OneOf(podTypeStrings()...),
				},
			},
			"key": schema.StringAttribute{
				MarkdownDescription: "Pod key. Defaults to `config`, the platform's convention for the single per-entity notification config. Override only if you know the bucket is keyed differently.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(db_vars.NotificationConfigKey),
				PlanModifiers:       requiresReplace,
			},

			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Turns this notification on for this entity.",
				Required:            true,
			},
			"muted": schema.BoolAttribute{
				MarkdownDescription: "Force-disables delivery regardless of broader scopes. An absolute veto — a mute at any scope wins over an enable at every other.",
				Optional:            true,
			},
			"muted_until": schema.StringAttribute{
				MarkdownDescription: "Timed mute, RFC3339. Muted while now is before this instant, then auto-clears with no write. A past value reads as not muted.",
				Optional:            true,
			},
			"urgency": schema.Int64Attribute{
				MarkdownDescription: "Urgency level, 1 (lowest) to 5 (highest). Omit to inherit from a broader scope or the type default.",
				Optional:            true,
				Validators: []validator.Int64{
					int64validator.Between(entity_pods.MinNotificationUrgency, entity_pods.MaxNotificationUrgency),
				},
			},
			"mode": schema.StringAttribute{
				MarkdownDescription: "When to fire, for outcome-based types. `transition` fires only on the edge (pass→fail); `all` fires on every evaluation. Ignored by computed/periodic types.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.OneOf(string(pkgvars.FireModeTransition), string(pkgvars.FireModeAll)),
				},
			},
			"recipients": schema.ListAttribute{
				MarkdownDescription: "Role-scoped audiences to notify. One or more of: " + recipientDocList() + ". Roles resolve against the entity at delivery time, so a notification on a control can target the control's, the containing gate's, or the asset's audience.",
				Optional:            true,
				ElementType:         types.StringType,
				Validators: []validator.List{
					listvalidator.ValueStringsAre(stringvalidator.OneOf(recipientStrings()...)),
				},
			},
			"channels": schema.ListAttribute{
				MarkdownDescription: "Delivery channels. One or more of: " + channelDocList() + ". `email` and `in_app` are always available; the rest require the tenant to have connected an integration.",
				Optional:            true,
				ElementType:         types.StringType,
				Validators: []validator.List{
					listvalidator.ValueStringsAre(stringvalidator.OneOf(channelStrings()...)),
				},
			},

			"rules":   rulesAttribute(),
			"params":  paramsAttribute(),
			"filters": filtersAttribute(),
		},
	}
}

func rulesAttribute() schema.SingleNestedAttribute {
	attr := base.CriteriaAttribute("notification")
	attr.MarkdownDescription = "Restricts which assets this notification fires for. Omit to fire for every asset in the entity's scope. Same shape as `fianu_policy` variation criteria and `fianu_gate` check matching — a CEL expression group, a reference to existing `fianu_index` entities, or a bare asset type."
	return attr
}

func paramsAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Type-specific tuning knobs. Each field applies to a subset of buckets; anything left unset falls back to the engine default.",
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"lead_window_days": schema.Int64Attribute{
				MarkdownDescription: "How many days ahead to warn. `notification_exception_expiring` only.",
				Optional:            true,
			},
			"duration_days": schema.Int64Attribute{
				MarkdownDescription: "How long a failure must persist before firing. `notification_persistent_failure` only.",
				Optional:            true,
			},
			"window_days": schema.Int64Attribute{
				MarkdownDescription: "Look-back window. `notification_repeated_exception` and the trend buckets.",
				Optional:            true,
			},
			"min_count": schema.Int64Attribute{
				MarkdownDescription: "Minimum occurrences within `window_days` before firing. `notification_repeated_exception` only.",
				Optional:            true,
			},
			"threshold_pct": schema.Int64Attribute{
				MarkdownDescription: "Percentage threshold that triggers the notification. Trend buckets and `notification_gate_readiness`.",
				Optional:            true,
			},
			"min_assets": schema.Int64Attribute{
				MarkdownDescription: "Noise floor — skip the notification when fewer than this many assets are involved. Trend buckets only.",
				Optional:            true,
			},
			"control_scope": schema.StringAttribute{
				MarkdownDescription: "Default coverage for per-control surfaces. `all` includes every control unless its own pod opts out; `selected` includes only controls whose own pod opts in. `notification_scm_pull_request` only.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.OneOf(
						string(entity_pods.ControlScopeAll),
						string(entity_pods.ControlScopeSelected),
					),
				},
			},
		},
	}
}

func filtersAttribute() schema.MapNestedAttribute {
	return schema.MapNestedAttribute{
		MarkdownDescription: "Per-sub-event toggles, keyed by the bucket's catalog toggle key (e.g. `on_any_commit`, `tagged_commits`, `gate_blocking`, `release_blocking`, `recovery`). Read verbatim by the server: a key you do not list is a filter left **off**, so list every sub-event you want enabled. Only meaningful on `notification_attestation_failure`.",
		Optional:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"enabled": schema.BoolAttribute{
					MarkdownDescription: "Whether this sub-event fires.",
					Required:            true,
				},
				"urgency": schema.Int64Attribute{
					MarkdownDescription: "Per-sub-event urgency, 1-5. Omit to inherit the catalog toggle's default.",
					Optional:            true,
					Validators: []validator.Int64{
						int64validator.Between(entity_pods.MinNotificationUrgency, entity_pods.MaxNotificationUrgency),
					},
				},
			},
		},
	}
}

func (r *notificationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *notificationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan notificationModel
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

// Read confirms the pod still exists and evicts state if it does not. It does
// not hydrate the config back: the server canonicalises the value (CEL
// expressions get compiled and rewritten into index references, in particular),
// so echoing it into state would surface permanent false drift. Same rule the
// entity resources follow for their Detail sections.
func (r *notificationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state notificationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.GetEntityPod(ctx, state.EntityUUID.ValueString(), state.Type.ValueString(), state.podKey())
	if err != nil {
		if entitypod.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch notification pod failed", err.Error())
		return
	}

	state.ID = types.StringValue(entitypod.FormatID(state.EntityUUID.ValueString(), state.Type.ValueString(), state.podKey()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *notificationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan notificationModel
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

func (r *notificationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state notificationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteEntityPod(ctx, state.EntityUUID.ValueString(), state.Type.ValueString(), state.podKey())
	if err != nil && !entitypod.IsNotFound(err) {
		resp.Diagnostics.AddError("delete notification pod failed", err.Error())
	}
}

func (r *notificationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	entityUUID, podType, key, err := entitypod.ParseID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	if !db_vars.PodType(podType).IsValid() || !isNotificationPodType(podType) {
		resp.Diagnostics.AddError("invalid import id",
			fmt.Sprintf("%q is not a notification pod type; import it as fianu_entity_pod instead", podType))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("entity_uuid"), entityUUID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("type"), podType)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), key)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// podKey returns the configured key, defaulting to the platform's
// NotificationConfigKey convention.
func (m *notificationModel) podKey() string {
	if k := m.Key.ValueString(); k != "" {
		return k
	}
	return db_vars.NotificationConfigKey
}

// set upserts the pod. SetEntityPod is a PUT, so Create and Update share it.
func (r *notificationResource) set(ctx context.Context, plan *notificationModel) diag.Diagnostics {
	cfg, diags := buildConfig(plan)
	if diags.HasError() {
		return diags
	}

	// Run the server's own validator locally so a bad urgency/mode/recipient
	// fails with a precise message at apply time instead of a generic 400.
	if err := cfg.Validate(); err != nil {
		diags.AddError("invalid notification config", err.Error())
		return diags
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		diags.AddError("marshal notification config failed", err.Error())
		return diags
	}

	key := plan.podKey()
	pod := entitypod.Build(plan.Type.ValueString(), key, raw, types.StringNull(), types.StringNull(), plan.Enabled)
	if _, err := r.client.SetEntityPod(ctx, plan.EntityUUID.ValueString(), pod.PodType, pod.Key, pod); err != nil {
		diags.AddError("set notification pod failed", err.Error())
		return diags
	}

	plan.ID = types.StringValue(entitypod.FormatID(plan.EntityUUID.ValueString(), pod.PodType, key))
	return diags
}

func buildConfig(m *notificationModel) (entity_pods.NotificationConfig, diag.Diagnostics) {
	var diags diag.Diagnostics

	cfg := entity_pods.NotificationConfig{
		Enabled: m.Enabled.ValueBool(),
		Muted:   m.Muted.ValueBool(),
		Urgency: int(m.Urgency.ValueInt64()),
		Mode:    pkgvars.FireMode(m.Mode.ValueString()),
		Rules:   m.Rules.ToEntity(),
	}

	if s := m.MutedUntil.ValueString(); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			diags.AddAttributeError(path.Root("muted_until"), "muted_until is not a valid RFC3339 timestamp", err.Error())
			return cfg, diags
		}
		cfg.MutedUntil = &t
	}

	for _, v := range m.Recipients {
		cfg.Recipients = append(cfg.Recipients, pkgvars.Recipient(v.ValueString()))
	}
	for _, v := range m.Channels {
		cfg.Channels = append(cfg.Channels, pkgvars.Channel(v.ValueString()))
	}

	if p := m.Params; p != nil {
		cfg.Params = entity_pods.NotificationParams{
			LeadWindowDays: optionalInt(p.LeadWindowDays),
			DurationDays:   optionalInt(p.DurationDays),
			WindowDays:     optionalInt(p.WindowDays),
			MinCount:       optionalInt(p.MinCount),
			ThresholdPct:   optionalInt(p.ThresholdPct),
			MinAssets:      optionalInt(p.MinAssets),
		}
		if s := p.ControlScope.ValueString(); s != "" {
			scope := entity_pods.ControlScope(s)
			cfg.Params.ControlScope = &scope
		}
	}

	if len(m.Filters) > 0 {
		cfg.Filters = make(map[string]entity_pods.FilterToggle, len(m.Filters))
		for key, f := range m.Filters {
			cfg.Filters[key] = entity_pods.FilterToggle{
				Enabled: f.Enabled.ValueBool(),
				Urgency: int(f.Urgency.ValueInt64()),
			}
		}
	}

	return cfg, diags
}

func optionalInt(v types.Int64) *int {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	i := int(v.ValueInt64())
	return &i
}
