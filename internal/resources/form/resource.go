// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package form implements the fianu_form Terraform resource.
//
// A form is a reusable questionnaire definition: an ordered list of elements
// that a human fills in when attesting. The form entity is the *definition*
// only. The filled-in answers are a form instance, which belongs to whatever
// attestation collected them and is not managed here.
//
// Wire shape:
//
//	name: ...
//	path: ...
//	type: form
//	detail:
//	  displayKey: ...
//	  description: ...
//	  elements:
//	    - name: ...
//	      type: text|blob|boolean|radio|checkbox
//	      required: true
//	      description: ...
//	      options: {...}
//
// `entities.Form` embeds `StandardEntity[FormDetail]` (it is not a type alias
// like Environment), so Detail is reached through the embedded field.
package form

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

const entityType = "form"

// elementTypes is the set the server can actually construct. It is deliberately
// narrower than the form_element_type SQL enum, which also lists `dropdown`:
// FormElement.GetAsFormElementImpl has no case for DropdownMenu and returns
// "unsupported form element type", so a dropdown fails inside InsertForm with
// no attribute path attached. Enumerating the buildable set here turns that
// into a plan-time error on the right line. Add `dropdown` when the server
// grows an ElementDropdown, not before.
var elementTypes = []string{
	string(db_vars.TextInput),
	string(db_vars.MultiLineInput),
	string(db_vars.BooleanToggle),
	string(db_vars.RadioButton),
	string(db_vars.Checkboxes),
}

// Compile-time interface checks.
var (
	_ resource.Resource                   = (*formResource)(nil)
	_ resource.ResourceWithConfigure      = (*formResource)(nil)
	_ resource.ResourceWithImportState    = (*formResource)(nil)
	_ resource.ResourceWithIdentity       = (*formResource)(nil)
	_ resource.ResourceWithValidateConfig = (*formResource)(nil)
)

// NewResource is the factory the provider package registers.
func NewResource() resource.Resource {
	return &formResource{}
}

type formResource struct {
	client *sdk.Client
}

type formModel struct {
	base.EnvelopeModel
	Detail formDetailModel `tfsdk:"detail"`
}

type formDetailModel struct {
	DisplayKey  types.String       `tfsdk:"display_key"`
	Description types.String       `tfsdk:"description"`
	Elements    []formElementModel `tfsdk:"elements"`
}

type formElementModel struct {
	Name        types.String `tfsdk:"name"`
	Type        types.String `tfsdk:"type"`
	Required    types.Bool   `tfsdk:"required"`
	Description types.String `tfsdk:"description"`
	Options     types.String `tfsdk:"options"`
}

func (r *formResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_form"
}

func (r *formResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = detailAttribute()
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Fianu form — the reusable questionnaire definition an attestation presents. This resource manages the form *definition*; the answers someone submits are a form instance and belong to the attestation that collected them.",
		Attributes:          attrs,
	}
}

func detailAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Form payload — mirrors the spec.yaml structure used by `fianu console deploy`.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"display_key": schema.StringAttribute{
				MarkdownDescription: "Short key shown in the console alongside the form name.",
				Optional:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description of what this form collects.",
				Optional:            true,
			},
			"elements": schema.ListNestedAttribute{
				MarkdownDescription: "The form's questions, in the order they are presented. **Order is meaningful**: the server assigns each element a `code` from its position, and answers are matched to questions by that code. Reordering elements renumbers them, which re-points any answers already collected — append rather than insert.",
				Required:            true,
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							MarkdownDescription: "The question text.",
							Required:            true,
						},
						"type": schema.StringAttribute{
							MarkdownDescription: "Element type. One of `text` (single line), `blob` (multi-line), `boolean` (toggle), `radio` (pick one), `checkbox` (pick many).",
							Required:            true,
							Validators: []validator.String{
								stringvalidator.OneOf(elementTypes...),
							},
						},
						"required": schema.BoolAttribute{
							MarkdownDescription: "Whether an answer must be supplied. Defaults to `false`.",
							Optional:            true,
						},
						"description": schema.StringAttribute{
							MarkdownDescription: "Helper text shown under the question.",
							Optional:            true,
						},
						"options": schema.StringAttribute{
							MarkdownDescription: "Element configuration as a JSON object, authored with `jsonencode({...})`. The accepted keys depend on `type`: `text` takes `{validation, expression}` (expression is a Go regexp, required when validation is true), `blob` takes `{placeholder}`, and `boolean`, `radio` and `checkbox` take `{values = {...}}`. The server prunes unknown keys.",
							Optional:            true,
						},
					},
				},
			},
		},
	}
}

func (r *formResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = base.EnvelopeIdentitySchema()
}

func (r *formResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ValidateConfig catches a malformed `options` at plan time. Without it a
// typo'd jsonencode deploys an element with no options at all, which surfaces
// later as a question that silently accepts anything.
func (r *formResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg formModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for i, e := range cfg.Detail.Elements {
		if e.Options.IsNull() || e.Options.IsUnknown() || e.Options.ValueString() == "" {
			continue
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(e.Options.ValueString()), &parsed); err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("detail").AtName("elements").AtListIndex(i).AtName("options"),
				"value is not a JSON object",
				"Author this attribute with jsonencode({...}). Parse error: "+err.Error(),
			)
		}
	}
}

func (r *formResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan formModel
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

func (r *formResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state formModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fetched, err := r.client.FetchForm(ctx, state.Path.ValueString(), nil, nil)
	if err != nil {
		// Only a real 404 evicts state. Other errors (network, 5xx, transient
		// auth) surface as a diagnostic so apply doesn't silently drop a
		// resource that still exists server-side.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch form failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrateFromForm(ctx, &state, fetched)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&state))...)
}

func (r *formResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan formModel
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

func (r *formResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state formModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	uuid := state.UUID.ValueString()
	if uuid == "" {
		return
	}
	if _, err := r.client.ArchiveForm(ctx, uuid); err != nil {
		// 404 means it's already gone — happy path for destroy.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("archive form failed", err.Error())
	}
}

func (r *formResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
	// Pre-populate detail so the post-import Read can decode: formModel.Detail
	// is a value type and the framework refuses to convert null into it. Same
	// fix as control, policy and tool. `elements` is Required, so it comes back
	// empty and the user's HCL supplies it on the next plan.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("detail"), formDetailModel{
		DisplayKey:  types.StringNull(),
		Description: types.StringNull(),
		Elements:    []formElementModel{},
	})...)
}

// applyPlan is the shared Create/Update body: deploy, then refetch so the
// sparse DeploymentMetadata gets supplemented with the full version envelope.
// Falls back to the deploy metadata if the refetch fails — a form that
// deployed but can't be read back is still in state, so destroy can reach it.
func (r *formResource) applyPlan(ctx context.Context, plan *formModel) diag.Diagnostics {
	deployResp, diags := r.deploy(ctx, *plan)
	if diags.HasError() {
		return diags
	}
	fetched, err := r.client.FetchForm(ctx, plan.Path.ValueString(), nil, nil)
	if err != nil {
		return hydrateFromDeployResponse(ctx, plan, deployResp)
	}
	return hydrateFromForm(ctx, plan, fetched)
}

func (r *formResource) deploy(ctx context.Context, plan formModel) (*transportv1.DeployEntityFileResponse, diag.Diagnostics) {
	var diags diag.Diagnostics
	entityJSON, err := json.Marshal(buildEntity(plan))
	if err != nil {
		diags.AddError("marshal form failed", err.Error())
		return nil, diags
	}
	entityTypeStr := string(db_vars.EntityTypeForm)
	entityPath := plan.Path.ValueString()
	deployResp, err := r.client.DeployEntityFile(ctx, transportv1.DeployEntityFileRequest{
		General: fianu.General{EntityType: &entityTypeStr, Path: &entityPath},
	}, entityJSON, false)
	if err != nil {
		diags.AddError("deploy form failed", err.Error())
		return nil, diags
	}
	return deployResp, diags
}

// buildEntity translates the HCL model into the wire entity.
//
// The lifecycle fields are set explicitly. The form deployer defaults an
// unspecified form to draft/inactive, matching the console's create endpoint
// where a form is authored before it is published — but the form read filters
// to active, so a Terraform-managed form left at that default would 404 on the
// very next Read and be evicted from state. A form declared in HCL is a
// declaration that it should be live, so the resource says so on the wire
// rather than the deployer diverging from the API path.
//
// Element `code` and `uuid` are deliberately not settable: Form.Validate
// assigns code from the element's position and regenerates uuid on every write,
// so anything sent here is overwritten. List order is the only ordering input.
func buildEntity(plan formModel) *fianu_entities.Form {
	f := &fianu_entities.Form{}
	f.Path = plan.Path.ValueString()
	f.Name = plan.Name.ValueString()
	f.Type = db_vars.EntityTypeForm
	f.Version.State = db_vars.EntityStatePublished
	f.Version.Status = db_vars.EntityStatusActive

	f.Detail.DisplayKey = plan.Detail.DisplayKey.ValueString()
	if v := plan.Detail.Description; !v.IsNull() && !v.IsUnknown() {
		s := v.ValueString()
		f.Detail.Description = &s
	}

	f.Detail.Elements = make([]*fianu_entities.FormElement, 0, len(plan.Detail.Elements))
	for i, e := range plan.Detail.Elements {
		f.Detail.Elements = append(f.Detail.Elements, &fianu_entities.FormElement{
			Name:        e.Name.ValueString(),
			Code:        i,
			Type:        db_vars.FormElementType(e.Type.ValueString()),
			Required:    e.Required.ValueBool(),
			Description: e.Description.ValueString(),
			Options:     decodeJSONObject(e.Options),
		})
	}
	return f
}

// decodeJSONObject returns nil for absent or unparseable input. ValidateConfig
// has already reported a parse failure with an attribute path by the time this
// runs, so returning nil here cannot swallow the error.
func decodeJSONObject(v types.String) map[string]any {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(v.ValueString()), &out); err != nil {
		return nil
	}
	return out
}

func hydrateFromDeployResponse(ctx context.Context, m *formModel, resp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	if resp == nil || resp.Metadata == nil {
		return nil
	}
	env := base.EnvelopeFromDeployMetadata(entityType, resp.Metadata, m.Path.ValueString(), m.Name.ValueString())
	return m.Hydrate(ctx, env)
}

// hydrateFromForm populates envelope state only. Detail stays user-authored:
// the server sorts elements by code, stamps a fresh uuid on each one and prunes
// `options` down to the keys the element type recognises, so hydrating them
// would surface drift on every plan. Same rule as control, policy and tool.
func hydrateFromForm(ctx context.Context, m *formModel, f *fianu_entities.Form) diag.Diagnostics {
	if f == nil {
		return nil
	}
	return m.Hydrate(ctx, base.EnvelopeFromStandardEntity(entityType, &f.StandardEntity))
}

type identityModel struct {
	EntityType types.String `tfsdk:"entity_type"`
	EntityKey  types.String `tfsdk:"entity_key"`
	UUID       types.String `tfsdk:"uuid"`
}

func makeIdentity(m *formModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}
