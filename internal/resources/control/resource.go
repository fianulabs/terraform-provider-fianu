// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	fianu_types "github.com/fianulabs/core/v2/external/db/types/fianu"
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"

	// fianu is the legacy client package; we still rely on it solely for the
	// in-memory ControlBuilder which builds *fianu_entities.Control structs.
	// All HTTP traffic now goes through external/pkg/sdk/v2 below.
	fianu "github.com/fianulabs/core/v2/external/pkg/clients/fianu"
	sdk "github.com/fianulabs/core/v2/external/pkg/sdk/v2"
	transportv1 "github.com/fianulabs/core/v2/external/transport/http/v1"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/fianulabs/terraform-provider-fianu/internal/resources/base"
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
	client *sdk.Client
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

func (r *controlResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan controlModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	deployResp, diags := r.deployControl(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.hydrateAfterDeploy(ctx, &plan, deployResp)...)
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

	fetched, err := r.client.FetchControl(ctx, state.Path.ValueString(), nil, nil)
	if err != nil {
		// Only a real 404 evicts state. Any other error (network, 5xx,
		// transient auth) surfaces as a diagnostic so terraform apply doesn't
		// silently drop a resource that still exists server-side.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch control failed", err.Error())
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
	var plan, state controlModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	deployResp, diags := r.deployControl(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.hydrateAfterDeploy(ctx, &plan, deployResp)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// UUID is immutable for a given control path. The server's partial-update
	// path returns an empty EntityID in DeploymentMetadata, which would clobber
	// the identity and trigger Terraform's "Unexpected Identity Change" guard.
	// Carry the prior state's UUID through so the identity stays stable.
	if plan.UUID.IsNull() || plan.UUID.IsUnknown() || plan.UUID.ValueString() == "" {
		plan.UUID = state.UUID
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
	if _, err := r.client.ArchiveControl(ctx, uuid); err != nil {
		// 404 means it's already gone — happy path for destroy.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("archive control failed", err.Error())
		return
	}
}

// deployControl is the shared Create/Update body. Marshals the entity to JSON,
// builds the General envelope, and POSTs to /api/entities/artifacts/deploy.
// Same SHA256 idempotency contract as the on-disk-package path the CLI uses.
func (r *controlResource) deployControl(ctx context.Context, plan controlModel) (*transportv1.DeployEntityFileResponse, diag.Diagnostics) {
	var diags diag.Diagnostics
	entity, err := buildEntity(plan)
	if err != nil {
		diags.AddError("invalid control configuration", err.Error())
		return nil, diags
	}
	entityJSON, err := json.Marshal(entity)
	if err != nil {
		diags.AddError("marshal entity failed", err.Error())
		return nil, diags
	}
	entityType := string(db_vars.EntityTypeControl)
	path := plan.Path.ValueString()
	deployReq := transportv1.DeployEntityFileRequest{
		General: fianu_types.General{
			EntityType: &entityType,
			Path:       &path,
		},
	}
	deployResp, err := r.client.DeployEntityFile(ctx, deployReq, entityJSON, false)
	if err != nil {
		diags.AddError("deploy control failed", err.Error())
		return nil, diags
	}
	return deployResp, diags
}

func (r *controlResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
	// Pre-populate the detail object so the subsequent Read's req.State.Get
	// can decode without choking on a null nested object — controlModel.Detail
	// is a value type, not a pointer, so the framework refuses to convert null
	// into it. Read overwrites these with the server-fetched values; any
	// user-authored sections (relations/assets/evaluation/etc.) come from
	// the HCL on the next plan/apply.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("detail"), controlDetailModel{
		FullName:    types.StringNull(),
		DisplayKey:  types.StringNull(),
		Description: types.StringNull(),
	})...)
}

// buildEntity translates the Terraform model into the entity-side Control by
// delegating to fianu.NewControlBuilder — the SDK builder is the single
// source of truth for *fianu_entities.Control construction so that the
// on-disk YAML deployer (pkg/controls/files.go::BuildControlFromFiles) and
// this provider can converge on one path.
func buildEntity(plan controlModel) (*fianu_entities.Control, error) {
	b := fianu.NewControlBuilder(plan.Path.ValueString(), plan.Name.ValueString()).
		WithControlInfo(
			plan.Detail.FullName.ValueString(),
			plan.Detail.DisplayKey.ValueString(),
			stringPtr(plan.Detail.Description),
		).
		WithDocumentation(toDocumentations(plan.Detail.Documentation)...).
		WithEvaluation(buildEvaluationCases(plan.Detail.Evaluation)...)

	if plan.Detail.Results != nil {
		b = b.WithResults(toResults(plan.Detail.Results))
	}
	for _, rel := range plan.Detail.Relations {
		b = b.WithRelation(rel.toRelation())
	}
	for _, a := range plan.Detail.Assets {
		b = b.WithAsset(a.toAsset())
	}
	if plan.Detail.PolicyTemplate != nil {
		b = b.WithMeasures(buildMeasures(plan.Detail.PolicyTemplate.Measures)).
			WithPolicyTemplateVersion(plan.Detail.PolicyTemplate.Version.ValueString())
	}
	if plan.Detail.Config != nil {
		b = b.WithConfig(fianu_entities.ControlConfig{
			Scope:              plan.Detail.Config.Scope.ValueString(),
			Retries:            plan.Detail.Config.Retries.ValueBool(),
			EvidenceSubmission: plan.Detail.Config.EvidenceSubmission.ValueBool(),
			ManualAttestations: plan.Detail.Config.ManualAttestations.ValueBool(),
		})
	}

	return b.Build()
}

// toDocumentations is plumbing: the model and entity field shapes match
// 1:1, so the loop is just types.String → string.
func toDocumentations(in []documentationModel) []fianu_entities.Documentation {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.Documentation, len(in))
	for i, d := range in {
		out[i] = fianu_entities.Documentation{Title: d.Title.ValueString(), URL: d.URL.ValueString()}
	}
	return out
}

// toResults converts the typed model into the server's map[string]bool. The
// keys are the entities.ResultKey* typed constants — the only place magic
// strings for these keys appear in the codebase. Cast to string at the
// callsite because Results is map[string]bool.
func toResults(in *resultsModel) fianu_entities.Results {
	out := fianu_entities.Results{}
	setIf := func(key fianu_entities.ResultKey, v types.Bool) {
		if !v.IsNull() && !v.IsUnknown() {
			out[string(key)] = v.ValueBool()
		}
	}
	setIf(fianu_entities.ResultKeyPass, in.Pass)
	setIf(fianu_entities.ResultKeyFail, in.Fail)
	setIf(fianu_entities.ResultKeyNotRequired, in.NotRequired)
	setIf(fianu_entities.ResultKeyInProgress, in.InProgress)
	setIf(fianu_entities.ResultKeyWarn, in.Warn)
	return out
}

func hydrateFromDeployResponse(ctx context.Context, m *controlModel, resp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	if resp == nil || resp.Metadata == nil {
		return nil
	}
	env := base.EnvelopeFromDeployMetadata(entityType, resp.Metadata, m.Path.ValueString(), m.Name.ValueString())
	return m.Hydrate(ctx, env)
}

// hydrateAfterDeploy is the post-deploy state population used by Create and
// Update. The /entities/artifacts/deploy response carries a sparse
// DeploymentMetadata (entityId + path + name + version.semantic only) — the
// rest of the version envelope (uuid, timestamp, status, state) only exists
// on the full Control returned by FetchControl. Without this refetch, Update
// returns state where those fields are empty and trips the framework's
// "Provider produced inconsistent result after apply" guard, because the
// plan has them pinned to prior state values.
//
// Falls back to the DeploymentMetadata-only hydrate when the refetch fails so
// a successful deploy isn't blown away by a transient read error.
func (r *controlResource) hydrateAfterDeploy(ctx context.Context, m *controlModel, deployResp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	fetched, err := r.client.FetchControl(ctx, m.Path.ValueString(), nil, nil)
	if err != nil {
		return hydrateFromDeployResponse(ctx, m, deployResp)
	}
	return hydrateFromControl(ctx, m, fetched)
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
	env := base.EnvelopeFromStandardEntity(entityType, &c.StandardEntity)
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
