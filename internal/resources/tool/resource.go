// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package tool implements the fianu_tool Terraform resource.
//
// A tool is an integration entity: the thing that emits the evidence a control
// evaluates. Registering one declares what it produces (and consumes) so the
// server can validate the graph — a control's evaluation input has to come from
// somewhere, and `sources` is where that somewhere is declared.
//
// Wire shape:
//
//	name: ...
//	path: ...
//	type: tool
//	detail:
//	  description: ...
//	  key: ...
//	  toolType: ...
//	  toolVersion: ...
//	  sources:
//	    produces: [{ path: ..., note: ..., integration: {...} }]
//	    consumes: [...]
//
// `entities.Tool` embeds `StandardEntity[ToolDetail]` (it is not a type alias
// like Environment), so Detail is reached through the embedded field and the
// entity type is stamped on `t.StandardEntity.Type`.
package tool

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

const entityType = "tool"

// Compile-time interface checks.
var (
	_ resource.Resource                   = (*toolResource)(nil)
	_ resource.ResourceWithConfigure      = (*toolResource)(nil)
	_ resource.ResourceWithImportState    = (*toolResource)(nil)
	_ resource.ResourceWithIdentity       = (*toolResource)(nil)
	_ resource.ResourceWithValidateConfig = (*toolResource)(nil)
)

// NewResource is the factory the provider package registers.
func NewResource() resource.Resource {
	return &toolResource{}
}

type toolResource struct {
	client *sdk.Client
}

type toolModel struct {
	base.EnvelopeModel
	Detail toolDetailModel `tfsdk:"detail"`
}

type toolDetailModel struct {
	Description types.String       `tfsdk:"description"`
	Key         types.String       `tfsdk:"key"`
	ToolType    types.String       `tfsdk:"tool_type"`
	ToolVersion types.String       `tfsdk:"tool_version"`
	Sources     *base.SourcesModel `tfsdk:"sources"`
}

func (r *toolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tool"
}

func (r *toolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = detailAttribute()
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Fianu tool — an integration that produces the evidence controls evaluate. Declaring a tool's `sources.produces` is what makes its output addressable by a control's evaluation input.",
		Attributes:          attrs,
	}
}

func detailAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Tool payload — mirrors the spec.yaml structure used by `fianu console deploy`.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description of what this tool does.",
				Optional:            true,
			},
			"key": schema.StringAttribute{
				MarkdownDescription: "Short tool key, e.g. `checkmarx`. Distinct from `path`: the key identifies the tool product, the path identifies this entity.",
				Optional:            true,
			},
			"tool_type": schema.StringAttribute{
				MarkdownDescription: "Category of tool, e.g. `sast`, `sca`, `container_scan`. Free-form — the server does not enumerate these.",
				Optional:            true,
			},
			"tool_version": schema.StringAttribute{
				MarkdownDescription: "Version of the tool product itself (not the entity version). Required — the server rejects a tool without it.",
				Required:            true,
			},
			"sources": base.SourcesAttribute(),
		},
	}
}

func (r *toolResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = base.EnvelopeIdentitySchema()
}

func (r *toolResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ValidateConfig surfaces a malformed `sources[].schema` at plan time rather
// than letting BuildSources drop it silently.
func (r *toolResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg toolModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(base.SourcesValidationDiags(path.Root("detail").AtName("sources"), cfg.Detail.Sources)...)
}

func (r *toolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan toolModel
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

func (r *toolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state toolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fetched, err := r.client.FetchTool(ctx, state.Path.ValueString())
	if err != nil {
		// Only a real 404 evicts state. Other errors (network, 5xx, transient
		// auth) surface as a diagnostic so apply doesn't silently drop a
		// resource that still exists server-side.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch tool failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrateFromTool(ctx, &state, fetched)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&state))...)
}

func (r *toolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan toolModel
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

func (r *toolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state toolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	uuid := state.UUID.ValueString()
	if uuid == "" {
		return
	}
	if _, err := r.client.ArchiveTool(ctx, uuid); err != nil {
		// 404 means it's already gone — happy path for destroy.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("archive tool failed", err.Error())
	}
}

func (r *toolResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
	// Pre-populate detail so the post-import Read can decode: toolModel.Detail
	// is a value type and the framework refuses to convert null into it. Same
	// fix as control and policy. tool_version is Required, so it carries a
	// placeholder the user's HCL replaces on the next plan.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("detail"), toolDetailModel{
		Description: types.StringNull(),
		Key:         types.StringNull(),
		ToolType:    types.StringNull(),
		ToolVersion: types.StringNull(),
	})...)
}

// applyPlan is the shared Create/Update body: deploy, then refetch so the
// sparse DeploymentMetadata gets supplemented with the full version envelope.
// Falls back to the deploy metadata if the refetch fails — a tool that
// deployed but can't be read back is still in state, so destroy can reach it.
func (r *toolResource) applyPlan(ctx context.Context, plan *toolModel) diag.Diagnostics {
	deployResp, diags := r.deploy(ctx, *plan)
	if diags.HasError() {
		return diags
	}
	fetched, err := r.client.FetchTool(ctx, plan.Path.ValueString())
	if err != nil {
		return hydrateFromDeployResponse(ctx, plan, deployResp)
	}
	return hydrateFromTool(ctx, plan, fetched)
}

func (r *toolResource) deploy(ctx context.Context, plan toolModel) (*transportv1.DeployEntityFileResponse, diag.Diagnostics) {
	var diags diag.Diagnostics
	entityJSON, err := json.Marshal(buildEntity(plan))
	if err != nil {
		diags.AddError("marshal tool failed", err.Error())
		return nil, diags
	}
	entityTypeStr := string(db_vars.EntityTypeTool)
	entityPath := plan.Path.ValueString()
	deployResp, err := r.client.DeployEntityFile(ctx, transportv1.DeployEntityFileRequest{
		General: fianu.General{EntityType: &entityTypeStr, Path: &entityPath},
	}, entityJSON, false)
	if err != nil {
		diags.AddError("deploy tool failed", err.Error())
		return nil, diags
	}
	return deployResp, diags
}

// buildEntity translates the HCL model into the wire entity. Tool embeds
// StandardEntity[ToolDetail] rather than aliasing it, so Detail is reached
// through the embedded field — but `Type` promotes cleanly, unlike on
// entities.Policy where the dual embed makes the same selector ambiguous.
func buildEntity(plan toolModel) *fianu_entities.Tool {
	t := &fianu_entities.Tool{}
	t.Path = plan.Path.ValueString()
	t.Name = plan.Name.ValueString()
	t.Type = db_vars.EntityTypeTool

	t.Detail.Description = plan.Detail.Description.ValueString()
	t.Detail.Key = plan.Detail.Key.ValueString()
	t.Detail.ToolType = plan.Detail.ToolType.ValueString()
	t.Detail.ToolVersion = plan.Detail.ToolVersion.ValueString()
	t.Detail.Sources = base.BuildSources(plan.Detail.Sources)
	return t
}

func hydrateFromDeployResponse(ctx context.Context, m *toolModel, resp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	if resp == nil || resp.Metadata == nil {
		return nil
	}
	env := base.EnvelopeFromDeployMetadata(entityType, resp.Metadata, m.Path.ValueString(), m.Name.ValueString())
	return m.Hydrate(ctx, env)
}

// hydrateFromTool populates envelope state only. Detail stays user-authored:
// the server applies defaults and rewrites `sources` entries as it resolves
// integration references to UUIDs, so hydrating them would surface drift on
// every plan. Same rule as control, policy and environment.
func hydrateFromTool(ctx context.Context, m *toolModel, t *fianu_entities.Tool) diag.Diagnostics {
	if t == nil {
		return nil
	}
	return m.Hydrate(ctx, base.EnvelopeFromStandardEntity(entityType, &t.StandardEntity))
}

type identityModel struct {
	EntityType types.String `tfsdk:"entity_type"`
	EntityKey  types.String `tfsdk:"entity_key"`
	UUID       types.String `tfsdk:"uuid"`
}

func makeIdentity(m *toolModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}
