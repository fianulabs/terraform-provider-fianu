// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package policy implements the fianu_policy Terraform resource.
//
// A policy is the bridge between a control (which defines compliance evaluation
// logic) and the assets it gets applied to. The resource shape mirrors the
// on-disk spec.yaml used by `fianu console deploy`:
//
//   general:
//     policy: { name, type, path }
//   control: { path }
//   policy:                   # array of variations
//     - effect: apply|exempt
//       priority: 0
//       policy: { ... }       # arbitrary key→value metric overrides
//   override:
//     asset:
//       types: [...]
//       explicit: [...]
//
// Wire-format parity: this resource produces a *fianu_entities.Policy which the
// server consumes identically to a YAML/JSON deploy from the CLI. Idempotency
// is server-driven (SHA256 hash of the entity content); `terraform apply`
// against an unchanged plan returns action="skipped" and doesn't bump the
// version.
package policy

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
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/fianulabs/terraform-provider-fianu/internal/resources/base"
)

const entityType = "policy"

// Compile-time interface checks.
var (
	_ resource.Resource                = (*policyResource)(nil)
	_ resource.ResourceWithConfigure   = (*policyResource)(nil)
	_ resource.ResourceWithImportState = (*policyResource)(nil)
	_ resource.ResourceWithIdentity    = (*policyResource)(nil)
)

// NewResource is the factory the provider package registers.
func NewResource() resource.Resource {
	return &policyResource{}
}

type policyResource struct {
	client *sdk.Client
}

// policyModel is the Terraform-side state. The envelope is shared via
// embedding; Detail carries the per-resource fields.
type policyModel struct {
	base.EnvelopeModel
	Detail policyDetailModel `tfsdk:"detail"`
}

// policyDetailModel mirrors fianu_entities.PolicyDetail minus the envelope-ish
// pieces (which live on EnvelopeModel) and the heavier optional sections
// (expiration, justification, assets, form) — those will be added in
// follow-up minor versions once a customer needs them.
type policyDetailModel struct {
	// Type maps to General.Policy.Type — one of standard/exception/target.
	Type types.String `tfsdk:"type"`

	// Control is the control this policy attaches to. Resolved server-side by
	// path; the EntityID is optional and only useful for pinning across
	// renames.
	Control policyControlModel `tfsdk:"control"`

	// Variations encode the policy[] array. Each variation has an effect,
	// priority, and a JSON-encoded policy detail (arbitrary key→value map of
	// metric overrides — kept as a string because HCL can't express truly
	// dynamic schemas cleanly).
	Variations []variationModel `tfsdk:"variations"`

	// Override controls which asset types/instances the policy applies to.
	// Optional — when omitted, the server falls back to the control's asset
	// scope.
	Override *overrideModel `tfsdk:"override"`

	// Assets is the list of abstract asset-type paths the policy applies to
	// (e.g., ["repository"], ["module", "artifact"]). Required by the
	// server's PolicyIsValid; the provider auto-derives this from
	// override.asset.types when this field is omitted but override is set.
	Assets []types.String `tfsdk:"assets"`
}

type policyControlModel struct {
	Path     types.String `tfsdk:"path"`
	EntityID types.String `tfsdk:"entity_id"`
}

func (r *policyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_policy"
}

func (r *policyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = detailAttribute()
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Fianu compliance policy. Policies bind a control's evaluation logic to the asset scope it gets applied to, and let you parameterise the control via per-variation metric overrides.",
		Attributes:          attrs,
	}
}

func detailAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Policy payload — mirrors the spec.yaml structure used by `fianu console deploy`.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"type": schema.StringAttribute{
				MarkdownDescription: "Policy type. One of `standard`, `exception`, `target`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.OneOf(
						string(fianu_entities.PolicyTypeStandard),
						string(fianu_entities.PolicyTypeException),
						string(fianu_entities.PolicyTypeTarget),
					),
				},
			},
			"control": schema.SingleNestedAttribute{
				MarkdownDescription: "Reference to the control this policy applies. The control's evaluation logic runs against the assets in scope.",
				Required:            true,
				Attributes: map[string]schema.Attribute{
					"path": schema.StringAttribute{
						MarkdownDescription: "Entity key of the target control (e.g., `checkmarx.sast.vulnerabilities`).",
						Required:            true,
					},
					"entity_id": schema.StringAttribute{
						MarkdownDescription: "Optional UUID of the target control. Resolved from `path` when omitted; set this only when pinning across renames.",
						Optional:            true,
					},
				},
			},
			"variations": variationsAttribute(),
			"override":   overrideAttribute(),
			"assets": schema.ListAttribute{
				MarkdownDescription: "Abstract asset-type paths the policy applies to (e.g., `[\"repository\"]`). Required by the server validator unless `override.asset.types` is set — when only override is supplied, the provider auto-derives this list from it.",
				Optional:            true,
				ElementType:         types.StringType,
			},
		},
	}
}

func (r *policyResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = base.EnvelopeIdentitySchema()
}

func (r *policyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *policyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan policyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	deployResp, diags := r.deployPolicy(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(hydrateFromDeployResponse(ctx, &plan, deployResp)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&plan))...)
}

func (r *policyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state policyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fetched, err := r.client.FetchPolicy(ctx, state.Path.ValueString(), nil, nil)
	if err != nil {
		// Only a real 404 evicts state. Other errors (network, 5xx,
		// transient auth) surface as a diagnostic so terraform apply doesn't
		// silently drop a resource that still exists server-side.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch policy failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrateFromPolicy(ctx, &state, fetched)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&state))...)
}

func (r *policyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan policyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	deployResp, diags := r.deployPolicy(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(hydrateFromDeployResponse(ctx, &plan, deployResp)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&plan))...)
}

func (r *policyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state policyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	uuid := state.UUID.ValueString()
	if uuid == "" {
		return
	}
	if _, err := r.client.ArchivePolicy(ctx, uuid); err != nil {
		// 404 means it's already gone — happy path for destroy.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("archive policy failed", err.Error())
		return
	}
}

func (r *policyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
}

// deployPolicy is the shared Create/Update body. Marshals the entity to JSON,
// builds the General envelope, and POSTs to /api/entities/artifacts/deploy.
func (r *policyResource) deployPolicy(ctx context.Context, plan policyModel) (*transportv1.DeployEntityFileResponse, diag.Diagnostics) {
	var diags diag.Diagnostics
	entity, err := buildEntity(plan)
	if err != nil {
		diags.AddError("invalid policy configuration", err.Error())
		return nil, diags
	}
	entityJSON, err := json.Marshal(entity)
	if err != nil {
		diags.AddError("marshal entity failed", err.Error())
		return nil, diags
	}
	entityTypeStr := string(db_vars.EntityTypePolicy)
	path := plan.Path.ValueString()
	deployReq := transportv1.DeployEntityFileRequest{
		General: fianu.General{
			EntityType: &entityTypeStr,
			Path:       &path,
		},
	}
	deployResp, err := r.client.DeployEntityFile(ctx, deployReq, entityJSON, false)
	if err != nil {
		diags.AddError("deploy policy failed", err.Error())
		return nil, diags
	}
	return deployResp, diags
}

// buildEntity translates the HCL model into a wire-side policy entity.
// Constructed directly because no fianu.NewPolicyBuilder exists yet — once
// it does, this is the natural place to switch over.
//
// Policy is a StandardEntity[PolicyDetail] just like Control, so the envelope
// (UUID/Path/Name/Type/Version) is shared with the rest of the entity
// ecosystem. The Detail fields (Type, Control, Variations, Override) live
// inline alongside the envelope on the wire — see entities.Policy's
// custom UnmarshalJSON for how the "type" key resolves to both EntityType
// and PolicyType.
func buildEntity(plan policyModel) (*fianu_entities.Policy, error) {
	p := &fianu_entities.Policy{}
	p.StandardEntity.Path = plan.Path.ValueString()
	p.StandardEntity.Name = plan.Name.ValueString()
	p.StandardEntity.Type = db_vars.EntityTypePolicy

	// Detail lives on StandardEntity[PolicyDetail].Detail (marshalled under
	// the JSON "detail" key). Policy ALSO directly embeds PolicyDetail at
	// the top level for legacy compat, but the nested-under-"detail" path
	// is what survives marshal/unmarshal cleanly.
	p.StandardEntity.Detail.Type = fianu_entities.PolicyType(plan.Detail.Type.ValueString())
	p.StandardEntity.Detail.Control = fianu_entities.PolicyControlRef{
		Path: plan.Detail.Control.Path.ValueString(),
	}
	if v := plan.Detail.Control.EntityID; !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		s := v.ValueString()
		p.StandardEntity.Detail.Control.EntityID = &s
	}
	p.StandardEntity.Detail.Variations = buildVariations(plan.Detail.Variations)

	if plan.Detail.Override != nil {
		p.StandardEntity.Detail.Override = plan.Detail.Override.toEntity()
	}

	// Detail.Assets is required by PolicyIsValid (entities/policy.go:967).
	// Prefer the explicit `assets` HCL field; fall back to mirroring
	// override.asset.types when only that's set.
	assets := plan.Detail.Assets
	if len(assets) == 0 && plan.Detail.Override != nil {
		assets = plan.Detail.Override.Asset.Types
	}
	for _, typePath := range assets {
		if typePath.IsNull() || typePath.IsUnknown() || typePath.ValueString() == "" {
			continue
		}
		p.StandardEntity.Detail.Assets = append(p.StandardEntity.Detail.Assets, fianu_entities.PolicyAssetRef{
			Path: typePath.ValueString(),
		})
	}

	return p, nil
}

// hydrateFromDeployResponse populates envelope state from the metadata that
// /entities/artifacts/deploy returns. Mirrors control's path.
func hydrateFromDeployResponse(ctx context.Context, m *policyModel, resp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	if resp == nil || resp.Metadata == nil {
		return nil
	}
	env := base.EnvelopeFromDeployMetadata(entityType, resp.Metadata, m.Path.ValueString(), m.Name.ValueString())
	return m.Hydrate(ctx, env)
}

// hydrateFromPolicy populates envelope state from the full Policy entity
// the SDK's FetchPolicy returns. Same Hydration Rule as the control
// resource: do NOT hydrate richer Detail sections (Variations, Override,
// etc.) — the server canonicalises ordering and applies defaults, which
// would surface as spurious drift on the next plan.
//
// Policy is StandardEntity[PolicyDetail] just like Control, so envelope
// hydration is a direct reuse of base.EnvelopeFromStandardEntity.
func hydrateFromPolicy(ctx context.Context, m *policyModel, p *fianu_entities.Policy) diag.Diagnostics {
	if p == nil {
		return nil
	}
	env := base.EnvelopeFromStandardEntity(entityType, &p.StandardEntity)
	return m.Hydrate(ctx, env)
}

type identityModel struct {
	EntityType types.String `tfsdk:"entity_type"`
	EntityKey  types.String `tfsdk:"entity_key"`
	UUID       types.String `tfsdk:"uuid"`
}

func makeIdentity(m *policyModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}
