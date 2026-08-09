// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package instance implements the fianu_instance Terraform resource.
//
// An instance is a configured, reachable deployment of a platform: the Jira at
// acme.atlassian.net, not "Jira" the product. A platform declares the
// operational contract; an instance says where it lives.
//
// Wire shape:
//
//	name: ...
//	path: ...
//	type: instance
//	detail:
//	  description: ...
//	  displayKey: ...
//	  platformUuid: ...
//	  domains:
//	    - host: ..., scheme: ..., designation: ..., utilities: [...]
//
// Domains are carried inline on the detail, not as child entities. A domain has
// no identity outside the instance version that declares it, so it is not in
// `children` — that is reserved for real entity-to-entity edges.
//
// Credentials are deliberately not managed here. They are secrets held in an
// external secret manager, with their own rotation lifecycle and their own
// endpoints; putting them in an entity version would mint a new instance
// version on every rotation and write secret references into entity history.
package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	fianu "github.com/fianulabs/core/v2/external/db/types/fianu"
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	sdk "github.com/fianulabs/core/v2/external/pkg/sdk/v2"
	transportv1 "github.com/fianulabs/core/v2/external/transport/http/v1"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/fianulabs/terraform-provider-fianu/internal/resources/base"
)

const entityType = "instance"

// domainSchemes is what the runtime can actually dial. Left unvalidated, a
// typo'd scheme deploys fine and fails at request time inside whatever job
// happens to use the domain first.
var domainSchemes = []string{"https", "http"}

// Compile-time interface checks.
var (
	_ resource.Resource                = (*instanceResource)(nil)
	_ resource.ResourceWithConfigure   = (*instanceResource)(nil)
	_ resource.ResourceWithImportState = (*instanceResource)(nil)
	_ resource.ResourceWithIdentity    = (*instanceResource)(nil)
)

// NewResource is the factory the provider package registers.
func NewResource() resource.Resource {
	return &instanceResource{}
}

type instanceResource struct {
	client *sdk.Client
}

type instanceModel struct {
	base.EnvelopeModel
	Detail instanceDetailModel `tfsdk:"detail"`
}

type instanceDetailModel struct {
	Description  types.String  `tfsdk:"description"`
	DisplayKey   types.String  `tfsdk:"display_key"`
	PlatformUUID types.String  `tfsdk:"platform_uuid"`
	Domains      []domainModel `tfsdk:"domains"`
}

type domainModel struct {
	Host        types.String   `tfsdk:"host"`
	Scheme      types.String   `tfsdk:"scheme"`
	Designation types.String   `tfsdk:"designation"`
	BasePath    types.String   `tfsdk:"base_path"`
	Utilities   []types.String `tfsdk:"utilities"`
	DisplayName types.String   `tfsdk:"display_name"`
	DisplayKey  types.String   `tfsdk:"display_key"`
	Description types.String   `tfsdk:"description"`
	ProxyURL    types.String   `tfsdk:"proxy_url"`
	Cache       types.Bool     `tfsdk:"cache"`
}

func (r *instanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (r *instanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = detailAttribute()
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Fianu integration instance — a configured, reachable deployment of a platform. Credentials are not managed here: they are secrets with their own rotation lifecycle and their own endpoints.",
		Attributes:          attrs,
	}
}

func detailAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Instance payload — mirrors the spec.yaml structure used by `fianu console deploy`.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"platform_uuid": schema.StringAttribute{
				MarkdownDescription: "Entity id (`uuid`) of the platform this is an instance of. Reference a managed platform as `fianu_platform.example.uuid`; the server derives the platform version from it.",
				Required:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description of this instance.",
				Optional:            true,
			},
			"display_key": schema.StringAttribute{
				MarkdownDescription: "Short key shown in the console. Defaults to the integration-key form of `path`.",
				Optional:            true,
				Computed:            true,
			},
			"domains": schema.ListNestedAttribute{
				MarkdownDescription: "Endpoints this instance is reachable at. A domain is not an entity — it has no identity outside the instance version that declares it, and the server stamps a fresh uuid on each one every time the instance is written.",
				Optional:            true,
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"host": schema.StringAttribute{
							MarkdownDescription: "Hostname, e.g. `acme.atlassian.net`. No scheme, no path.",
							Required:            true,
						},
						"scheme": schema.StringAttribute{
							MarkdownDescription: "URL scheme: `https` or `http`.",
							Required:            true,
							Validators: []validator.String{
								stringvalidator.OneOf(domainSchemes...),
							},
						},
						"designation": schema.StringAttribute{
							MarkdownDescription: "What this domain is for, e.g. `api`, `ui`, `webhook`. Free-form — the server does not enumerate these, but the runtime selects domains by it.",
							Required:            true,
						},
						"base_path": schema.StringAttribute{
							MarkdownDescription: "Path prefix applied to every request against this domain, e.g. `/rest/api/3`.",
							Optional:            true,
						},
						"utilities": schema.ListAttribute{
							MarkdownDescription: "Capability tags the runtime matches on when choosing a domain for a job.",
							Optional:            true,
							ElementType:         types.StringType,
						},
						"display_name": schema.StringAttribute{
							MarkdownDescription: "Human-readable name for this domain in the console.",
							Optional:            true,
						},
						"display_key": schema.StringAttribute{
							MarkdownDescription: "Short key for this domain in the console.",
							Optional:            true,
						},
						"description": schema.StringAttribute{
							MarkdownDescription: "Free-form description of this domain.",
							Optional:            true,
						},
						"proxy_url": schema.StringAttribute{
							MarkdownDescription: "Optional HTTP CONNECT forward proxy used to reach this domain.",
							Optional:            true,
						},
						"cache": schema.BoolAttribute{
							MarkdownDescription: "Whether responses from this domain may be cached. Defaults to `false`.",
							Optional:            true,
						},
					},
				},
			},
		},
	}
}

func (r *instanceResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = base.EnvelopeIdentitySchema()
}

func (r *instanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *instanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.applyPlan(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&plan))...)
}

func (r *instanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state instanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fetched, err := r.client.FetchInstance(ctx, state.Path.ValueString(), nil)
	if err != nil {
		// Only a real 404 evicts state. Other errors (network, 5xx, transient
		// auth) surface as a diagnostic so apply doesn't silently drop a
		// resource that still exists server-side.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch instance failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrateFromInstance(ctx, &state, fetched)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&state))...)
}

func (r *instanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.applyPlan(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&plan))...)
}

func (r *instanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state instanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	uuid := state.UUID.ValueString()
	if uuid == "" {
		return
	}
	if _, err := r.client.ArchiveInstance(ctx, uuid); err != nil {
		// 404 means it's already gone — happy path for destroy.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("archive instance failed", err.Error())
	}
}

func (r *instanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
	// Pre-populate detail so the post-import Read can decode: instanceModel's
	// Detail is a value type and the framework refuses to convert null into it.
	// Same fix as control, policy, tool and form. platform_uuid is Required, so
	// the user's HCL supplies it on the next plan.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("detail"), instanceDetailModel{
		Description:  types.StringNull(),
		DisplayKey:   types.StringNull(),
		PlatformUUID: types.StringNull(),
		Domains:      []domainModel{},
	})...)
}

// applyPlan is the shared Create/Update body: deploy, then refetch so the
// sparse DeploymentMetadata gets supplemented with the full version envelope.
// Falls back to the deploy metadata if the refetch fails — an instance that
// deployed but can't be read back is still in state, so destroy can reach it.
func (r *instanceResource) applyPlan(ctx context.Context, plan *instanceModel) diag.Diagnostics {
	deployResp, diags := r.deploy(ctx, *plan)
	if diags.HasError() {
		return diags
	}
	fetched, err := r.client.FetchInstance(ctx, plan.Path.ValueString(), nil)
	if err != nil {
		return hydrateFromDeployResponse(ctx, plan, deployResp)
	}
	return hydrateFromInstance(ctx, plan, fetched)
}

func (r *instanceResource) deploy(ctx context.Context, plan instanceModel) (*transportv1.DeployEntityFileResponse, diag.Diagnostics) {
	var diags diag.Diagnostics
	entityJSON, err := json.Marshal(buildEntity(plan))
	if err != nil {
		diags.AddError("marshal instance failed", err.Error())
		return nil, diags
	}
	entityTypeStr := string(db_vars.EntityTypeInstance)
	entityPath := plan.Path.ValueString()
	deployResp, err := r.client.DeployEntityFile(ctx, transportv1.DeployEntityFileRequest{
		General: fianu.General{EntityType: &entityTypeStr, Path: &entityPath},
	}, entityJSON, false)
	if err != nil {
		diags.AddError("deploy instance failed", err.Error())
		return nil, diags
	}
	return deployResp, diags
}

// buildEntity translates the HCL model into the wire entity.
//
// Domain UUIDs are not settable and not sent: the server stamps a fresh one per
// domain on every write, because they identify a row in a per-version table
// rather than a thing with a life of its own.
func buildEntity(plan instanceModel) *fianu_entities.Instance {
	e := &fianu_entities.Instance{}
	e.Path = plan.Path.ValueString()
	e.Name = plan.Name.ValueString()
	e.Type = db_vars.EntityTypeInstance

	e.Detail.Description = plan.Detail.Description.ValueString()
	e.Detail.DisplayKey = plan.Detail.DisplayKey.ValueString()
	e.Detail.PlatformUUID = plan.Detail.PlatformUUID.ValueString()

	e.Detail.Domains = make([]fianu_entities.InstanceDomain, 0, len(plan.Detail.Domains))
	for _, d := range plan.Detail.Domains {
		e.Detail.Domains = append(e.Detail.Domains, fianu_entities.InstanceDomain{
			Host:        d.Host.ValueString(),
			Scheme:      d.Scheme.ValueString(),
			Designation: d.Designation.ValueString(),
			BasePath:    d.BasePath.ValueString(),
			Utilities:   stringSlice(d.Utilities),
			DisplayName: d.DisplayName.ValueString(),
			DisplayKey:  d.DisplayKey.ValueString(),
			Description: d.Description.ValueString(),
			ProxyURL:    d.ProxyURL.ValueString(),
			Cache:       d.Cache.ValueBool(),
		})
	}
	return e
}

func stringSlice(in []types.String) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v.IsNull() || v.IsUnknown() {
			continue
		}
		out = append(out, v.ValueString())
	}
	return out
}

func hydrateFromDeployResponse(ctx context.Context, m *instanceModel, resp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	if resp == nil || resp.Metadata == nil {
		return nil
	}
	env := base.EnvelopeFromDeployMetadata(entityType, resp.Metadata, m.Path.ValueString(), m.Name.ValueString())
	return m.Hydrate(ctx, env)
}

// hydrateFromInstance populates the envelope plus display_key, which is
// Computed because the server defaults it from the path. The rest of the detail
// stays user-authored: the server re-stamps domain UUIDs on every write and
// returns domains in table order, so hydrating them would surface drift on
// every plan. Same rule as control, policy, tool and form.
func hydrateFromInstance(ctx context.Context, m *instanceModel, e *fianu_entities.Instance) diag.Diagnostics {
	if e == nil {
		return nil
	}
	if e.Detail.DisplayKey != "" {
		m.Detail.DisplayKey = types.StringValue(e.Detail.DisplayKey)
	}
	return m.Hydrate(ctx, base.EnvelopeFromStandardEntity(entityType, &e.StandardEntity))
}

type identityModel struct {
	EntityType types.String `tfsdk:"entity_type"`
	EntityKey  types.String `tfsdk:"entity_key"`
	UUID       types.String `tfsdk:"uuid"`
}

func makeIdentity(m *instanceModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}
