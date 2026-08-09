// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package policy implements the fianu_policy and fianu_policy_exception
// Terraform resources. Both are the same server-side entity under two entity
// types — see kind.go for why that is two resources and not one attribute.
//
// A policy is the bridge between a control (which defines compliance evaluation
// logic) and the assets it gets applied to. The resource shape mirrors the
// canonical entity YAML used by `fianu console deploy`:
//
//	name: ...
//	path: ...
//	type: policy
//	detail:
//	  type: standard|target        # or `exception` via fianu_policy_exception
//	  control:
//	    path: ...
//	  policy:                   # array of variations
//	    - effect: apply|exempt
//	      priority: 0
//	      criteria:
//	        asset: { type: repository }          # inline asset binding
//	        expressions: [{ expression: "..." }] # OR
//	        indexes: [{ path: "..." }]           # reference existing index
//	      policy: { ... }       # arbitrary key→value metric overrides
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
	sdk "github.com/fianulabs/core/v2/external/pkg/sdk/v2"
	transportv1 "github.com/fianulabs/core/v2/external/transport/http/v1"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/fianulabs/terraform-provider-fianu/internal/resources/base"
)

// Compile-time interface checks.
var (
	_ resource.Resource                   = (*policyResource)(nil)
	_ resource.ResourceWithConfigure      = (*policyResource)(nil)
	_ resource.ResourceWithImportState    = (*policyResource)(nil)
	_ resource.ResourceWithIdentity       = (*policyResource)(nil)
	_ resource.ResourceWithValidateConfig = (*policyResource)(nil)
)

// NewResource is the factory the provider package registers for fianu_policy.
func NewResource() resource.Resource {
	return &policyResource{kind: standardKind}
}

// NewExceptionResource is the factory the provider package registers for
// fianu_policy_exception.
func NewExceptionResource() resource.Resource {
	return &policyResource{kind: exceptionKind}
}

type policyResource struct {
	client *sdk.Client
	kind   policyKind
}

// policyModel is the Terraform-side state. The envelope is shared via
// embedding; Detail carries the per-resource fields.
type policyModel struct {
	base.EnvelopeModel
	Detail policyDetailModel `tfsdk:"detail"`
}

// policyDetailModel mirrors fianu_entities.PolicyDetail minus the envelope-ish
// pieces (which live on EnvelopeModel) and the heavier optional sections
// (expiration, justification, form) — those will be added in follow-up
// minor versions once a customer needs them.
type policyDetailModel struct {
	// Type maps to Detail.Type. Legal values are kind-dependent — see
	// policyKind.allowedPolicyTypes.
	Type types.String `tfsdk:"type"`

	// Control is the control this policy attaches to. Resolved server-side by
	// path; the EntityID is optional and only useful for pinning across
	// renames.
	Control policyControlModel `tfsdk:"control"`

	// Variations encode the policy[] array. Each variation has an effect,
	// priority, an optional criteria (asset / expressions / indexes), and a
	// JSON-encoded policy detail (arbitrary key→value map of metric
	// overrides — kept as a string because HCL can't express truly dynamic
	// schemas cleanly).
	Variations []variationModel `tfsdk:"variations"`
}

type policyControlModel struct {
	Path     types.String `tfsdk:"path"`
	EntityID types.String `tfsdk:"entity_id"`
}

func (r *policyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.kind.typeNameSuffix
}

func (r *policyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = detailAttribute(r.kind)
	resp.Schema = schema.Schema{
		MarkdownDescription: r.kind.resourceDescription,
		Attributes:          attrs,
	}
}

func detailAttribute(k policyKind) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Policy payload — mirrors the spec.yaml structure used by `fianu console deploy`.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"type": typeAttribute(k),
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
		},
	}
}

// typeAttribute renders `detail.type` per kind. The exception kind has exactly
// one legal value, so it is Optional+Computed with a default rather than
// Required — writing `type = "exception"` on a fianu_policy_exception is pure
// ceremony, but leaving the attribute out entirely would make the two kinds
// need separate model structs for no gain.
func typeAttribute(k policyKind) schema.StringAttribute {
	attr := schema.StringAttribute{
		MarkdownDescription: k.typeDescription,
		Required:            k.defaultPolicyType == "",
		Validators:          []validator.String{stringvalidator.OneOf(k.allowedPolicyTypes...)},
	}
	if k.defaultPolicyType != "" {
		attr.Optional = true
		attr.Computed = true
		attr.Default = stringdefault.StaticString(k.defaultPolicyType)
	}
	return attr
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
	resp.Diagnostics.Append(hydrateFromDeployResponse(ctx, r.kind, &plan, deployResp)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(r.kind, &plan))...)
}

func (r *policyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state policyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fetched, err := r.kind.fetch(ctx, r.client, state.Path.ValueString())
	if err != nil {
		// Only a real 404 evicts state. Other errors (network, 5xx,
		// transient auth) surface as a diagnostic so terraform apply doesn't
		// silently drop a resource that still exists server-side.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch "+r.kind.entityType+" failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrateFromPolicy(ctx, r.kind, &state, fetched)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(r.kind, &state))...)
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
	resp.Diagnostics.Append(hydrateFromDeployResponse(ctx, r.kind, &plan, deployResp)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(r.kind, &plan))...)
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
	if err := r.kind.archive(ctx, r.client, uuid); err != nil {
		// 404 means it's already gone — happy path for destroy.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("archive "+r.kind.entityType+" failed", err.Error())
		return
	}
}

func (r *policyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, r.kind.entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
	// Pre-populate the detail object so the subsequent Read's req.State.Get
	// can decode without choking on a null nested object — policyModel.Detail
	// is a value type, not a pointer, so the framework refuses to convert null
	// into it. Same fix as the control resource. Read hydrates the envelope
	// only; the detail sections come from the user's HCL on the next plan.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("detail"), policyDetailModel{
		Type: types.StringValue(r.kind.importedPolicyType()),
		Control: policyControlModel{
			Path:     types.StringNull(),
			EntityID: types.StringNull(),
		},
		Variations: []variationModel{},
	})...)
}

// ValidateConfig enforces the policy-level binding rule from the server's
// `PolicyIsValid` (`core/external/db/types/fianu/entities/policy.go`): when
// `Detail.Assets` is empty, every variation must carry
// `criteria.asset.type` (`allVariationsHaveCriteriaAsset`). `fianu_policy`'s
// schema has no top-level assets/override attribute, so the only path that
// satisfies the rule is per-variation asset binding — including on
// indexes-only variations, which the per-criteria validator (which calls
// `PolicyAssetGroup.IsValid` directly) doesn't reject on its own.
//
// Mirrors the server check rather than calls it directly because
// `PolicyIsValid` depends on control entity-ID resolution (server-side) and
// would error on fields the provider can't populate at plan time.
func (r *policyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg policyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	variationsPath := path.Root("detail").AtName("variations")
	for i, v := range cfg.Detail.Variations {
		entry := variationsPath.AtListIndex(i)
		if v.Criteria == nil {
			resp.Diagnostics.AddAttributeError(
				entry,
				"variation missing criteria.asset.type",
				"fianu_policy has no top-level `assets`/`override` attribute, so every variation needs `criteria = { asset = { type = \"<asset_type>\" } }`. Without it the server rejects the deploy with `policy has no asset binding`.",
			)
			continue
		}
		if v.Criteria.Asset == nil || v.Criteria.Asset.Type.IsNull() || v.Criteria.Asset.Type.IsUnknown() || v.Criteria.Asset.Type.ValueString() == "" {
			resp.Diagnostics.AddAttributeError(
				entry.AtName("criteria"),
				"variation criteria missing asset.type",
				"fianu_policy has no top-level `assets`/`override` attribute, so every variation needs `criteria.asset.type` set (including indexes-only variations). Without it the server rejects the deploy with `policy has no asset binding`.",
			)
		}
	}
}

// deployPolicy is the shared Create/Update body. Marshals the entity to JSON,
// builds the General envelope, and POSTs to /api/entities/artifacts/deploy.
func (r *policyResource) deployPolicy(ctx context.Context, plan policyModel) (*transportv1.DeployEntityFileResponse, diag.Diagnostics) {
	var diags diag.Diagnostics
	entity, err := buildEntity(r.kind, plan)
	if err != nil {
		diags.AddError("invalid "+r.kind.entityType+" configuration", err.Error())
		return nil, diags
	}
	entityJSON, err := json.Marshal(entity)
	if err != nil {
		diags.AddError("marshal entity failed", err.Error())
		return nil, diags
	}
	entityTypeStr := string(r.kind.dbType)
	path := plan.Path.ValueString()
	deployReq := transportv1.DeployEntityFileRequest{
		General: fianu.General{
			EntityType: &entityTypeStr,
			Path:       &path,
		},
	}
	deployResp, err := r.client.DeployEntityFile(ctx, deployReq, entityJSON, false)
	if err != nil {
		diags.AddError("deploy "+r.kind.entityType+" failed", err.Error())
		return nil, diags
	}
	return deployResp, diags
}

// buildEntity translates the HCL model into a wire-side policy entity.
//
// Policy is a StandardEntity[PolicyDetail] just like Control, so the envelope
// (UUID/Path/Name/Type/Version) is shared with the rest of the entity
// ecosystem. The Detail fields (Type, Control, Variations) live inline
// alongside the envelope on the wire — see entities.Policy's custom
// UnmarshalJSON for how the "type" key resolves to both EntityType and
// PolicyType.
//
// Scope: post-2026-06 the wire shape carries asset binding per criteria
// (criteria.asset.type / criteria.indexes). The legacy top-level
// detail.assets[] and detail.override blocks were removed; the server
// synthesizes Detail.Assets on read from the union of per-criteria asset
// types for legacy display consumers.
func buildEntity(k policyKind, plan policyModel) (*fianu_entities.Policy, error) {
	p := &fianu_entities.Policy{}
	p.Path = plan.Path.ValueString()
	p.Name = plan.Name.ValueString()
	p.StandardEntity.Type = k.dbType

	p.Detail.Type = fianu_entities.PolicyType(plan.Detail.Type.ValueString())
	p.Detail.Control = fianu_entities.PolicyControlRef{
		Path: plan.Detail.Control.Path.ValueString(),
	}
	if v := plan.Detail.Control.EntityID; !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		s := v.ValueString()
		p.Detail.Control.EntityID = &s
	}
	p.Detail.Variations = buildVariations(plan.Detail.Variations)

	return p, nil
}

// hydrateFromDeployResponse populates envelope state from the metadata that
// /entities/artifacts/deploy returns. Mirrors control's path.
func hydrateFromDeployResponse(ctx context.Context, k policyKind, m *policyModel, resp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	if resp == nil || resp.Metadata == nil {
		return nil
	}
	env := base.EnvelopeFromDeployMetadata(k.entityType, resp.Metadata, m.Path.ValueString(), m.Name.ValueString())
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
func hydrateFromPolicy(ctx context.Context, k policyKind, m *policyModel, p *fianu_entities.Policy) diag.Diagnostics {
	if p == nil {
		return nil
	}
	env := base.EnvelopeFromStandardEntity(k.entityType, &p.StandardEntity)
	return m.Hydrate(ctx, env)
}

type identityModel struct {
	EntityType types.String `tfsdk:"entity_type"`
	EntityKey  types.String `tfsdk:"entity_key"`
	UUID       types.String `tfsdk:"uuid"`
}

func makeIdentity(k policyKind, m *policyModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(k.entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}
