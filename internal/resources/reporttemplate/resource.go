// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package reporttemplate implements the fianu_report_template Terraform
// resource.
//
// A report template composes control templates into a full report layout —
// which sections appear, in what order, with which header and footer blocks.
//
// Wire shape:
//
//	name: ...
//	path: ...
//	type: template
//	detail:
//	  description: ...
//	  layoutConfig: { ... }        # free-form JSON
//	  outputFormats: [pdf, html, json]
//
// `entities.ReportTemplate` is a plain type alias for
// `StandardEntity[ReportTemplateDetail]`, like Environment — Detail is reached
// directly, no embedded field to navigate.
//
// Note the entity type is `template`, not `report_template`: the server's
// EntityTypeReportTemplate constant is the string "template", so that is what
// appears in the composite resource ID and the import prefix.
package reporttemplate

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

// entityType is the string the server uses, not the Go constant's name.
var entityType = string(db_vars.EntityTypeReportTemplate)

// Compile-time interface checks.
var (
	_ resource.Resource                   = (*reportTemplateResource)(nil)
	_ resource.ResourceWithConfigure      = (*reportTemplateResource)(nil)
	_ resource.ResourceWithImportState    = (*reportTemplateResource)(nil)
	_ resource.ResourceWithIdentity       = (*reportTemplateResource)(nil)
	_ resource.ResourceWithValidateConfig = (*reportTemplateResource)(nil)
)

// NewResource is the factory the provider package registers.
func NewResource() resource.Resource {
	return &reportTemplateResource{}
}

type reportTemplateResource struct {
	client *sdk.Client
}

type reportTemplateModel struct {
	base.EnvelopeModel
	Detail reportTemplateDetailModel `tfsdk:"detail"`
}

type reportTemplateDetailModel struct {
	Description   types.String `tfsdk:"description"`
	LayoutConfig  types.String `tfsdk:"layout_config"`
	OutputFormats []string     `tfsdk:"output_formats"`
}

func (r *reportTemplateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_report_template"
}

func (r *reportTemplateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = detailAttribute()
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Fianu report template — the layout that composes control templates into a full report. The entity type on the wire is `template`, so imports and the resource `id` use the `template/` prefix.",
		Attributes:          attrs,
	}
}

func detailAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Report template payload.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description of what this report covers.",
				Optional:            true,
			},
			"layout_config": schema.StringAttribute{
				MarkdownDescription: "Which control templates to include, section ordering, header/footer blocks and grouping rules, as a JSON object. Author with `jsonencode({...})` — the shape is defined by the reporting engine and validated server-side.",
				Optional:            true,
			},
			"output_formats": schema.ListAttribute{
				MarkdownDescription: "Formats this template can render to. Defaults server-side to `[\"pdf\", \"html\", \"json\"]` when omitted.",
				Optional:            true,
				ElementType:         types.StringType,
				Validators: []validator.List{
					listvalidator.ValueStringsAre(stringvalidator.OneOf("pdf", "html", "json")),
				},
			},
		},
	}
}

func (r *reportTemplateResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = base.EnvelopeIdentitySchema()
}

func (r *reportTemplateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ValidateConfig rejects a layout_config that isn't a JSON object at plan
// time. Without it a typo'd jsonencode deploys a template with an empty
// layout, which renders an empty report rather than failing.
func (r *reportTemplateResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg reportTemplateModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v := cfg.Detail.LayoutConfig
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(v.ValueString()), &parsed); err != nil {
		resp.Diagnostics.AddAttributeError(
			path.Root("detail").AtName("layout_config"),
			"layout_config is not a JSON object",
			"Author it with jsonencode({...}). Parse error: "+err.Error(),
		)
	}
}

func (r *reportTemplateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan reportTemplateModel
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

func (r *reportTemplateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state reportTemplateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fetched, err := r.client.FetchTemplate(ctx, state.Path.ValueString(), nil)
	if err != nil {
		// Only a real 404 evicts state. Other errors surface as a diagnostic
		// so apply doesn't silently drop a resource that still exists.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch report template failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrateFromTemplate(ctx, &state, fetched)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&state))...)
}

func (r *reportTemplateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan reportTemplateModel
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

func (r *reportTemplateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state reportTemplateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	uuid := state.UUID.ValueString()
	if uuid == "" {
		return
	}
	if _, err := r.client.ArchiveTemplate(ctx, uuid); err != nil {
		// 404 means it's already gone — happy path for destroy.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("archive report template failed", err.Error())
	}
}

func (r *reportTemplateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
	// Pre-populate detail so the post-import Read can decode — the model's
	// Detail is a value type and the framework refuses null for it.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("detail"), reportTemplateDetailModel{
		Description:  types.StringNull(),
		LayoutConfig: types.StringNull(),
	})...)
}

// applyPlan is the shared Create/Update body: deploy, then refetch so the
// sparse DeploymentMetadata gets supplemented with the full version envelope.
func (r *reportTemplateResource) applyPlan(ctx context.Context, plan *reportTemplateModel) diag.Diagnostics {
	deployResp, diags := r.deploy(ctx, *plan)
	if diags.HasError() {
		return diags
	}
	fetched, err := r.client.FetchTemplate(ctx, plan.Path.ValueString(), nil)
	if err != nil {
		return hydrateFromDeployResponse(ctx, plan, deployResp)
	}
	return hydrateFromTemplate(ctx, plan, fetched)
}

func (r *reportTemplateResource) deploy(ctx context.Context, plan reportTemplateModel) (*transportv1.DeployEntityFileResponse, diag.Diagnostics) {
	var diags diag.Diagnostics
	entityJSON, err := json.Marshal(buildEntity(plan))
	if err != nil {
		diags.AddError("marshal report template failed", err.Error())
		return nil, diags
	}
	entityTypeStr := entityType
	entityPath := plan.Path.ValueString()
	deployResp, err := r.client.DeployEntityFile(ctx, transportv1.DeployEntityFileRequest{
		General: fianu.General{EntityType: &entityTypeStr, Path: &entityPath},
	}, entityJSON, false)
	if err != nil {
		diags.AddError("deploy report template failed", err.Error())
		return nil, diags
	}
	return deployResp, diags
}

// buildEntity translates the HCL model into the wire entity.
func buildEntity(plan reportTemplateModel) *fianu_entities.ReportTemplate {
	e := &fianu_entities.ReportTemplate{}
	e.Path = plan.Path.ValueString()
	e.Name = plan.Name.ValueString()
	e.Type = db_vars.EntityTypeReportTemplate

	if v := plan.Detail.Description; !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		s := v.ValueString()
		e.Detail.Description = &s
	}
	if v := plan.Detail.LayoutConfig; !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		// Safe to ignore the parse result: ValidateConfig already rejected
		// anything that isn't a JSON object, so this string has parsed once.
		e.Detail.LayoutConfig = json.RawMessage(v.ValueString())
	}
	e.Detail.OutputFormats = plan.Detail.OutputFormats
	return e
}

func hydrateFromDeployResponse(ctx context.Context, m *reportTemplateModel, resp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	if resp == nil || resp.Metadata == nil {
		return nil
	}
	env := base.EnvelopeFromDeployMetadata(entityType, resp.Metadata, m.Path.ValueString(), m.Name.ValueString())
	return m.Hydrate(ctx, env)
}

// hydrateFromTemplate populates envelope state only. Detail stays
// user-authored: the server defaults OutputFormats and may re-serialise
// LayoutConfig with different key ordering, either of which would read as
// drift. Same rule as every other entity resource.
func hydrateFromTemplate(ctx context.Context, m *reportTemplateModel, e *fianu_entities.ReportTemplate) diag.Diagnostics {
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

func makeIdentity(m *reportTemplateModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}
