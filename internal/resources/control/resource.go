// Package control implements the fianu_control Terraform resource.
//
// The schema mirrors the production spec.yaml structure used by official
// Fianu controls (see /Users/noahkreiger/Documents/fianulabs/core/official-controls/
// CLAUDE.md for the source-of-truth reference). Every spec.yaml field is a
// first-class HCL attribute; rego/python content lives in `evaluation[].content`
// strings (typically loaded via `file("${path.module}/rule.rego")`).
//
// Console-deploy parity: the on-disk control package (spec.yaml + rule.rego +
// detail.py + display.py + tests + fixtures) and an HCL fianu_control resource
// produce identical Control entities on the server. The CLI tars the directory
// into a multipart upload; the provider builds the same *fianu_entities.Control
// in Go and JSON-marshals it. Both paths terminate at
// pkg/entities_files/control_deployer.go::DeployFromRawContent and honour the
// same SHA256 idempotency gate at service.go:183-201.
package control

import (
	"context"
	"fmt"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	fianu "github.com/fianulabs/core/v2/external/pkg/clients/fianu"
	transportv1 "github.com/fianulabs/core/v2/external/transport/http/v1"
	"github.com/fianulabs/terraform-provider-fianu/internal/resources/base"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const entityType = "control"

// Compile-time interface checks.
var (
	_ resource.Resource                = (*controlResource)(nil)
	_ resource.ResourceWithConfigure   = (*controlResource)(nil)
	_ resource.ResourceWithImportState = (*controlResource)(nil)
	_ resource.ResourceWithIdentity    = (*controlResource)(nil)
)

// NewResource is the factory the provider package registers.
func NewResource() resource.Resource {
	return &controlResource{}
}

type controlResource struct {
	client *fianu.Client
}

// controlModel is the Terraform-side state. The envelope is shared via
// embedding; Detail carries the per-resource fields.
type controlModel struct {
	base.EnvelopeModel
	Detail controlDetailModel `tfsdk:"detail"`
}

// controlDetailModel mirrors fianu_entities.ControlDetail. Each section is
// Optional except the ControlInfo trio (full_name/display_key/description),
// which are Required so existing v0.1 HCL keeps working.
type controlDetailModel struct {
	FullName    types.String `tfsdk:"full_name"`
	DisplayKey  types.String `tfsdk:"display_key"`
	Description types.String `tfsdk:"description"`

	Documentation  []documentationModel  `tfsdk:"documentation"`
	Results        *resultsModel         `tfsdk:"results"`
	Relations      []relationModel       `tfsdk:"relations"`
	Assets         []controlAssetModel   `tfsdk:"assets"`
	PolicyTemplate *policyTemplateModel  `tfsdk:"policy_template"`
	Evaluation     []evaluationCaseModel `tfsdk:"evaluation"`
	Config         *configModel          `tfsdk:"config"`
}

type documentationModel struct {
	Title types.String `tfsdk:"title"`
	URL   types.String `tfsdk:"url"`
}

// resultsModel mirrors entities.Results (which is `map[string]bool` server-side).
// Exposing it as named fields gives plan-time validation and IDE completion;
// only set fields are sent on the wire.
type resultsModel struct {
	Pass        types.Bool `tfsdk:"pass"`
	Fail        types.Bool `tfsdk:"fail"`
	NotRequired types.Bool `tfsdk:"not_required"`
	InProgress  types.Bool `tfsdk:"in_progress"`
	Warn        types.Bool `tfsdk:"warn"`
}

type policyTemplateModel struct {
	Version  types.String     `tfsdk:"version"`
	Measures []measureModelL1 `tfsdk:"measures"`
}

type configModel struct {
	Scope              types.String `tfsdk:"scope"`
	Retries            types.Bool   `tfsdk:"retries"`
	EvidenceSubmission types.Bool   `tfsdk:"evidence_submission"`
	ManualAttestations types.Bool   `tfsdk:"manual_attestations"`
}

func (r *controlResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_control"
}

func (r *controlResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = detailAttribute()
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Fianu compliance control. Controls define a compliance requirement together with its evaluation logic, policy template, asset scope, and data-source relations. The schema mirrors the on-disk control-package format used by `fianu console deploy`.",
		Attributes:          attrs,
	}
}

// detailAttribute returns the SingleNestedAttribute for `detail` carrying every
// section a real control authors. Kept in a builder so the acceptance tests
// can introspect the schema without instantiating the full resource.
func detailAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Control payload — mirrors the spec.yaml structure used by `fianu console deploy`.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"full_name":   schema.StringAttribute{Required: true, MarkdownDescription: "Display name (e.g., `Static Asset Security Analysis`)."},
			"display_key": schema.StringAttribute{Required: true, MarkdownDescription: "Short uppercase key (e.g., `CHXST`)."},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Free-form description of what the control validates."},

			"documentation": schema.ListNestedAttribute{
				MarkdownDescription: "External documentation links (vendor docs, runbooks).",
				Optional:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"title": schema.StringAttribute{Required: true},
						"url":   schema.StringAttribute{Required: true},
					},
				},
			},

			"results": schema.SingleNestedAttribute{
				MarkdownDescription: "Default result outcomes when the rule emits each verdict. Maps directly to `entities.Results` (a server-side `map[string]bool`).",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"pass":         schema.BoolAttribute{Optional: true},
					"fail":         schema.BoolAttribute{Optional: true},
					"not_required": schema.BoolAttribute{Optional: true},
					"in_progress":  schema.BoolAttribute{Optional: true},
					"warn":         schema.BoolAttribute{Optional: true},
				},
			},

			"relations": relationsAttribute(),
			"assets":    assetsAttribute(),

			"policy_template": schema.SingleNestedAttribute{
				MarkdownDescription: "Policy template — the schema users author policies against. `measures` is the recursive measure tree.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"version": schema.StringAttribute{
						MarkdownDescription: "Optional template version label.",
						Optional:            true,
					},
					"measures": measuresAttribute(),
				},
			},

			"evaluation": evaluationAttribute(),

			"config": schema.SingleNestedAttribute{
				MarkdownDescription: "Operational configuration — scope of evaluation, retry behavior, evidence/attestation flags.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"scope":               schema.StringAttribute{Optional: true},
					"retries":             schema.BoolAttribute{Optional: true},
					"evidence_submission": schema.BoolAttribute{Optional: true},
					"manual_attestations": schema.BoolAttribute{Optional: true},
				},
			},
		},
	}
}

func (r *controlResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = base.EnvelopeIdentitySchema()
}

func (r *controlResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*fianu.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"unexpected provider data",
			fmt.Sprintf("expected *fianu.Client, got %T. This is a provider bug.", req.ProviderData),
		)
		return
	}
	r.client = client
}

func (r *controlResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan controlModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deployResp, err := r.client.Deploy(ctx, fianu.DeployRequest{
		EntityType: db_vars.EntityTypeControl,
		Path:       plan.Path.ValueString(),
		Entity:     buildEntity(plan),
	})
	if err != nil {
		resp.Diagnostics.AddError("deploy control failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrateFromDeployResponse(ctx, &plan, deployResp)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&plan))...)
}

func (r *controlResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state controlModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fetched, err := r.client.FetchControl(state.Path.ValueString())
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(hydrateFromControl(ctx, &state, fetched)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&state))...)
}

func (r *controlResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan controlModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deployResp, err := r.client.Deploy(ctx, fianu.DeployRequest{
		EntityType: db_vars.EntityTypeControl,
		Path:       plan.Path.ValueString(),
		Entity:     buildEntity(plan),
	})
	if err != nil {
		resp.Diagnostics.AddError("deploy control failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrateFromDeployResponse(ctx, &plan, deployResp)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&plan))...)
}

func (r *controlResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state controlModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	uuid := state.UUID.ValueString()
	if uuid == "" {
		return
	}
	if err := r.client.ArchiveEntity(db_vars.EntityTypeControl, uuid); err != nil {
		resp.Diagnostics.AddError("archive control failed", err.Error())
		return
	}
}

func (r *controlResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
}

// buildEntity translates the Terraform model into the entity-side Control.
// Mirrors the file→struct mapping that pkg/controls/files.go::BuildControlFromFiles
// does for on-disk packages — the JSON we send and the YAML the CLI would send
// produce identical Control rows once they hit the server.
func buildEntity(plan controlModel) *fianu_entities.Control {
	c := &fianu_entities.Control{}
	c.Name = plan.Name.ValueString()
	c.Path = plan.Path.ValueString()
	c.Type = db_vars.EntityTypeControl

	c.Detail.Control = &fianu_entities.ControlInfo{
		FullName:    plan.Detail.FullName.ValueString(),
		DisplayKey:  plan.Detail.DisplayKey.ValueString(),
		Description: stringPtr(plan.Detail.Description),
	}

	if len(plan.Detail.Documentation) > 0 {
		c.Detail.Documentation = make([]fianu_entities.Documentation, len(plan.Detail.Documentation))
		for i, d := range plan.Detail.Documentation {
			c.Detail.Documentation[i] = fianu_entities.Documentation{
				Title: d.Title.ValueString(),
				URL:   d.URL.ValueString(),
			}
		}
	}

	if plan.Detail.Results != nil {
		c.Detail.Results = buildResults(plan.Detail.Results)
	}

	c.Detail.Relations = buildRelations(plan.Detail.Relations)
	c.Detail.Assets = buildAssets(plan.Detail.Assets)

	if plan.Detail.PolicyTemplate != nil {
		c.Detail.PolicyTemplate = fianu_entities.PolicyTemplate{
			Version:  plan.Detail.PolicyTemplate.Version.ValueString(),
			Measures: buildMeasures(plan.Detail.PolicyTemplate.Measures),
		}
	}

	c.Detail.Evaluation = buildEvaluationCases(plan.Detail.Evaluation)

	if plan.Detail.Config != nil {
		c.Detail.Config = fianu_entities.ControlConfig{
			Scope:              plan.Detail.Config.Scope.ValueString(),
			Retries:            plan.Detail.Config.Retries.ValueBool(),
			EvidenceSubmission: plan.Detail.Config.EvidenceSubmission.ValueBool(),
			ManualAttestations: plan.Detail.Config.ManualAttestations.ValueBool(),
		}
	}

	return c
}

// buildResults converts the typed model into the server's map[string]bool.
// Only fields the user explicitly set land in the map — leaving an unset
// field nil keeps the wire payload minimal and matches what the YAML deploy
// path produces.
func buildResults(in *resultsModel) fianu_entities.Results {
	out := fianu_entities.Results{}
	if !in.Pass.IsNull() && !in.Pass.IsUnknown() {
		out["pass"] = in.Pass.ValueBool()
	}
	if !in.Fail.IsNull() && !in.Fail.IsUnknown() {
		out["fail"] = in.Fail.ValueBool()
	}
	if !in.NotRequired.IsNull() && !in.NotRequired.IsUnknown() {
		out["notRequired"] = in.NotRequired.ValueBool()
	}
	if !in.InProgress.IsNull() && !in.InProgress.IsUnknown() {
		out["inProgress"] = in.InProgress.ValueBool()
	}
	if !in.Warn.IsNull() && !in.Warn.IsUnknown() {
		out["warn"] = in.Warn.ValueBool()
	}
	return out
}

func hydrateFromDeployResponse(ctx context.Context, m *controlModel, resp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	if resp == nil || resp.Metadata == nil {
		return nil
	}
	env := base.EntityEnvelope{
		EntityType:      entityType,
		EntityID:        resp.Metadata.EntityID,
		Path:            firstNonEmpty(resp.Metadata.Path, m.Path.ValueString()),
		Name:            firstNonEmpty(resp.Metadata.Name, m.Name.ValueString()),
		VersionSemantic: resp.Metadata.Version,
		Metadata:        map[string]string{},
		Parents:         []string{},
		Children:        []string{},
	}
	return m.Hydrate(ctx, env)
}

type identityModel struct {
	EntityType types.String `tfsdk:"entity_type"`
	EntityKey  types.String `tfsdk:"entity_key"`
	UUID       types.String `tfsdk:"uuid"`
}

func makeIdentity(m *controlModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}

// hydrateFromControl populates envelope fields and the ControlInfo trio from
// the server response. The richer Detail sections (documentation, relations,
// assets, measures, evaluation, config) intentionally stay user-authored —
// trusting the server's hash-idempotency gate at service.go:183-201 means we
// don't need to read them back. Reading them would risk drift if the server
// canonicalises ordering or applies defaults.
func hydrateFromControl(ctx context.Context, m *controlModel, c *fianu_entities.Control) diag.Diagnostics {
	if c == nil {
		return nil
	}
	env := base.EntityEnvelope{
		EntityType:       entityType,
		EntityID:         c.UUID,
		Path:             c.Path,
		Name:             c.Name,
		VersionSemantic:  c.Version.Semantic,
		VersionUUID:      c.Version.UUID,
		VersionStatus:    string(c.Version.Status),
		VersionState:     string(c.Version.State),
		VersionTimestamp: c.Version.Timestamp,
		Metadata:         map[string]string{},
		Parents:          []string{},
		Children:         []string{},
	}
	diags := m.Hydrate(ctx, env)

	if c.Detail.Control != nil {
		m.Detail.FullName = types.StringValue(c.Detail.Control.FullName)
		m.Detail.DisplayKey = types.StringValue(c.Detail.Control.DisplayKey)
		if c.Detail.Control.Description != nil {
			m.Detail.Description = types.StringValue(*c.Detail.Control.Description)
		} else {
			m.Detail.Description = types.StringNull()
		}
	}

	return diags
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
