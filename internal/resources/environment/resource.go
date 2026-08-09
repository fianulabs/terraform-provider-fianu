// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package environment implements the fianu_environment Terraform resource.
//
// An environment is a named deployment stage — dev, staging, production — that
// gates bind to and deployment events are matched against. Its `matching`
// block is a CEL expression group that decides which incoming deployment
// events belong to this environment.
//
// Wire shape:
//
//	name: ...
//	path: ...
//	type: environment
//	detail:
//	  description: ...
//	  documentation: [{ title: ..., url: ... }]
//	  matching:
//	    asset: { type: repository }
//	    expressions: [{ expression: "..." }]
//
// `entities.Environment` is a plain type alias for
// `StandardEntity[EnvironmentDetail]` — no dual embed, no custom
// (Un)MarshalJSON to work around. Detail fields go on `e.Detail` directly.
package environment

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
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/fianulabs/terraform-provider-fianu/internal/resources/base"
)

const entityType = "environment"

// Compile-time interface checks.
var (
	_ resource.Resource                = (*environmentResource)(nil)
	_ resource.ResourceWithConfigure   = (*environmentResource)(nil)
	_ resource.ResourceWithImportState = (*environmentResource)(nil)
	_ resource.ResourceWithIdentity    = (*environmentResource)(nil)
)

// NewResource is the factory the provider package registers.
func NewResource() resource.Resource {
	return &environmentResource{}
}

type environmentResource struct {
	client *sdk.Client
}

type environmentModel struct {
	base.EnvelopeModel
	Detail environmentDetailModel `tfsdk:"detail"`
}

type environmentDetailModel struct {
	Description   types.String        `tfsdk:"description"`
	Documentation []base.DocModel     `tfsdk:"documentation"`
	Matching      *base.CriteriaModel `tfsdk:"matching"`
}

func (r *environmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment"
}

func (r *environmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = detailAttribute()
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Fianu environment — a named deployment stage (dev, staging, production) that gates bind to and deployment events are matched against.",
		Attributes:          attrs,
	}
}

func detailAttribute() schema.SingleNestedAttribute {
	matching := base.CriteriaAttribute("environment")
	matching.MarkdownDescription = "CEL expression group deciding which deployment events belong to this environment. Omit to match unconditionally — useful for a catch-all environment. Same shape as `fianu_policy` variation criteria and `fianu_gate` check matching."

	return schema.SingleNestedAttribute{
		MarkdownDescription: "Environment payload.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description of what this environment represents.",
				Optional:            true,
			},
			"documentation": base.DocumentationAttribute(),
			"matching":      matching,
		},
	}
}

func (r *environmentResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = base.EnvelopeIdentitySchema()
}

func (r *environmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *environmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan environmentModel
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

func (r *environmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fetched, err := r.client.FetchEnvironment(ctx, state.Path.ValueString(), nil)
	if err != nil {
		// Only a real 404 evicts state. Other errors (network, 5xx, transient
		// auth) surface as a diagnostic so apply doesn't silently drop a
		// resource that still exists server-side.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch environment failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrateFromEnvironment(ctx, &state, fetched)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&state))...)
}

func (r *environmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan environmentModel
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

func (r *environmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	uuid := state.UUID.ValueString()
	if uuid == "" {
		return
	}
	if _, err := r.client.ArchiveEnvironment(ctx, uuid); err != nil {
		// 404 means it's already gone — happy path for destroy.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("archive environment failed", err.Error())
	}
}

func (r *environmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
	// Pre-populate detail so the post-import Read can decode: environmentModel's
	// Detail is a value type and the framework refuses to convert null into it.
	// Same fix as control, policy, tool, form and instance.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("detail"), environmentDetailModel{
		Description:   types.StringNull(),
		Documentation: []base.DocModel{},
	})...)
}

// applyPlan is the shared Create/Update body: deploy, then refetch so the
// sparse DeploymentMetadata gets supplemented with the full version envelope.
func (r *environmentResource) applyPlan(ctx context.Context, plan *environmentModel) diag.Diagnostics {
	deployResp, diags := r.deploy(ctx, *plan)
	if diags.HasError() {
		return diags
	}
	fetched, err := r.client.FetchEnvironment(ctx, plan.Path.ValueString(), nil)
	if err != nil {
		return hydrateFromDeployResponse(ctx, plan, deployResp)
	}
	return hydrateFromEnvironment(ctx, plan, fetched)
}

func (r *environmentResource) deploy(ctx context.Context, plan environmentModel) (*transportv1.DeployEntityFileResponse, diag.Diagnostics) {
	var diags diag.Diagnostics
	entityJSON, err := json.Marshal(buildEntity(plan))
	if err != nil {
		diags.AddError("marshal environment failed", err.Error())
		return nil, diags
	}
	entityTypeStr := string(db_vars.EntityTypeEnvironment)
	entityPath := plan.Path.ValueString()
	deployResp, err := r.client.DeployEntityFile(ctx, transportv1.DeployEntityFileRequest{
		General: fianu.General{EntityType: &entityTypeStr, Path: &entityPath},
	}, entityJSON, false)
	if err != nil {
		diags.AddError("deploy environment failed", err.Error())
		return nil, diags
	}
	return deployResp, diags
}

// buildEntity translates the HCL model into the wire entity. Environment is a
// type alias for StandardEntity[EnvironmentDetail], so Detail is unambiguous —
// no dual-embed to navigate.
func buildEntity(plan environmentModel) *fianu_entities.Environment {
	e := &fianu_entities.Environment{}
	e.Path = plan.Path.ValueString()
	e.Name = plan.Name.ValueString()
	e.Type = db_vars.EntityTypeEnvironment

	if v := plan.Detail.Description; !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		s := v.ValueString()
		e.Detail.Description = &s
	}
	e.Detail.Documentation = base.BuildDocumentation(plan.Detail.Documentation)
	e.Detail.Matching = plan.Detail.Matching.ToEntity()
	return e
}

func hydrateFromDeployResponse(ctx context.Context, m *environmentModel, resp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	if resp == nil || resp.Metadata == nil {
		return nil
	}
	env := base.EnvelopeFromDeployMetadata(entityType, resp.Metadata, m.Path.ValueString(), m.Name.ValueString())
	return m.Hydrate(ctx, env)
}

// hydrateFromEnvironment populates envelope state only. Detail stays
// user-authored — the server canonicalises `matching` (CEL expressions get
// compiled and rewritten into index references), so hydrating it would
// surface spurious drift on the next plan. Same rule as control and policy.
func hydrateFromEnvironment(ctx context.Context, m *environmentModel, e *fianu_entities.Environment) diag.Diagnostics {
	if e == nil {
		return nil
	}
	return m.Hydrate(ctx, base.EnvelopeFromStandardEntity(entityType, e))
}

type identityModel struct {
	EntityType types.String `tfsdk:"entity_type"`
	EntityKey  types.String `tfsdk:"entity_key"`
	UUID       types.String `tfsdk:"uuid"`
}

func makeIdentity(m *environmentModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}
