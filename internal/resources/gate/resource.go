// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package gate implements the fianu_gate Terraform resource.
//
// Gates are server-side `entities.Control` with `Type = "gate"`. Unlike
// controls, the server force-fills most of the Detail surface on every
// deploy (`pkg/controls/service.go::applyGateDefaults`):
//
//   - Evaluation cases default to `rules.FianuGateRuleV100` when none
//     are supplied.
//   - PolicyTemplate.Measures default to the matching rule measures.
//   - Relations and Assets are unconditionally force-set to the standard
//     gate transaction relation + repository/module/artifact asset binding.
//
// Exposing those fields in HCL would be misleading — anything a user
// authored would either be overwritten silently (relations/assets) or
// confusingly merged (evaluation/template). So the HCL surface intentionally
// drops them. What stays user-authored:
//
//   - The ControlInfo trio: full_name, display_key, description.
//   - Operational `config` (scope, retries, evidence/attestation flags).
//   - `environments` — gate-only entity-edge bindings to environment
//     entities (`entities.ControlDetail.Environments`).
//   - `policy` (nested) — an optional inline policy entity that the
//     provider deploys *separately* as a `fianu_entities.Policy` targeting
//     this gate. Practically every real gate has one, and authoring it
//     inline keeps the gate+policy lifecycle in a single HCL block.
//
// When the `policy` block is set the provider performs TWO deploys on
// every Create/Update: first the gate, then a policy entity whose
// `control.path` references the gate. Delete archives both in reverse
// order. Read fetches both and hydrates state. If the user wants multiple
// policies on the same gate, they author the additional ones via
// `fianu_policy` — the nested block is for the canonical default policy.
package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	fianu_types "github.com/fianulabs/core/v2/external/db/types/fianu"
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

const entityType = "gate"

// Compile-time interface checks.
var (
	_ resource.Resource                = (*gateResource)(nil)
	_ resource.ResourceWithConfigure   = (*gateResource)(nil)
	_ resource.ResourceWithImportState = (*gateResource)(nil)
	_ resource.ResourceWithIdentity    = (*gateResource)(nil)
)

// NewResource is the factory the provider package registers.
func NewResource() resource.Resource {
	return &gateResource{}
}

type gateResource struct {
	client *sdk.Client
}

// gateModel is the Terraform-side state. Envelope is shared via embedding;
// Detail carries the gate-specific fields.
type gateModel struct {
	base.EnvelopeModel
	Detail gateDetailModel `tfsdk:"detail"`
}

// gateDetailModel is the user-authored slice of a gate. Server-managed
// fields (evaluation, policy_template, relations, assets) are intentionally
// excluded — see package doc.
type gateDetailModel struct {
	FullName    types.String `tfsdk:"full_name"`
	DisplayKey  types.String `tfsdk:"display_key"`
	Description types.String `tfsdk:"description"`

	Config       *configModel          `tfsdk:"config"`
	Environments []environmentRefModel `tfsdk:"environments"`

	// Policy is the nested policy that gets deployed as a SEPARATE
	// `entities.Policy` entity targeting this gate. Optional — when nil,
	// the gate is deployed without an attached policy.
	Policy *gatePolicyModel `tfsdk:"policy"`

	// Pods is the list of pipeline automation rules attached to this gate
	// (server-side: `pod_type = "gate_check_rule"` rows scoped to the
	// gate's entity_id). Each pod's `Value` is a `GateCheckRuleValue` JSON
	// blob set via SetEntityPod.
	Pods []podModel `tfsdk:"pods"`

	// PolicyUUID is the computed UUID of the deployed policy entity (when
	// `policy` is set). Tracked so Delete can archive the policy by UUID.
	PolicyUUID types.String `tfsdk:"policy_uuid"`

	// PodKeys is the computed list of pod keys currently deployed against
	// this gate. Used at Update time to compute add/update/delete diffs
	// against the incoming Pods plan, and at Delete time to know which
	// pods to detach before archiving the gate.
	PodKeys types.List `tfsdk:"pod_keys"`
}

type configModel struct {
	Scope              types.String `tfsdk:"scope"`
	Retries            types.Bool   `tfsdk:"retries"`
	EvidenceSubmission types.Bool   `tfsdk:"evidence_submission"`
	ManualAttestations types.Bool   `tfsdk:"manual_attestations"`
}

// gatePolicyModel is the nested policy block. Mirrors the fianu_policy
// detail shape minus `control` (implicit — the policy targets the parent
// gate).
type gatePolicyModel struct {
	// Path is the policy entity's own path (e.g., "f.gate.security.policy").
	// Optional — defaults to `<gate.path>.policy` when omitted.
	Path types.String `tfsdk:"path"`
	// Name is the policy entity's display name. Optional — defaults to
	// `<gate.name>` when omitted.
	Name types.String `tfsdk:"name"`
	// Type is the policy type: standard / exception / target. Defaults to
	// "standard" when omitted.
	Type types.String `tfsdk:"type"`

	Variations []variationModel `tfsdk:"variations"`
	Override   *overrideModel   `tfsdk:"override"`

	// Assets is the list of abstract asset-type paths the policy applies
	// to. Required by the server validator unless override is supplied;
	// the provider auto-derives this from override.asset.types when only
	// override is set.
	Assets []types.String `tfsdk:"assets"`
}

func (r *gateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gate"
}

func (r *gateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = detailAttribute()
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Fianu gate. Gates bundle compliance evaluation into a composite gating decision; the server fills in the gate's evaluation logic, policy template, relations, and asset bindings from a fixed default (see `applyGateDefaults` in core/pkg/controls/service.go). The HCL surface only exposes the user-authored slice: gate identity, operational config, environment bindings, and a nested policy block. The nested `policy` deploys as a separate `entities.Policy` targeting this gate — for the gate to actually enforce anything, a policy must exist on it.",
		Attributes:          attrs,
	}
}

func detailAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Gate payload — identity, operational config, environment bindings, and (optionally) the inline policy.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"full_name":   schema.StringAttribute{Required: true, MarkdownDescription: "Display name (e.g., `Production Security Gate`)."},
			"display_key": schema.StringAttribute{Required: true, MarkdownDescription: "Short uppercase key (e.g., `PSEC`)."},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Free-form description of what the gate enforces."},

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

			"environments": environmentsAttribute(),

			"policy": schema.SingleNestedAttribute{
				MarkdownDescription: "Inline policy authored against this gate. The provider deploys this as a SEPARATE `fianu_entities.Policy` entity whose `control.path` references the gate. Optional — omit to deploy a gate with no attached policy (you'd then add policies via `fianu_policy` resources). Set this for the canonical gate+policy authoring flow.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"path": schema.StringAttribute{
						MarkdownDescription: "Policy entity path. Defaults to `<gate.path>.policy` when omitted.",
						Optional:            true,
					},
					"name": schema.StringAttribute{
						MarkdownDescription: "Policy display name. Defaults to the gate's `name` when omitted.",
						Optional:            true,
					},
					"type": schema.StringAttribute{
						MarkdownDescription: "Policy type. One of `standard`, `exception`, `target`. Defaults to `standard`.",
						Optional:            true,
						Validators: []validator.String{
							stringvalidator.OneOf(
								string(fianu_entities.PolicyTypeStandard),
								string(fianu_entities.PolicyTypeException),
								string(fianu_entities.PolicyTypeTarget),
							),
						},
					},
					"variations": variationsAttribute(),
					"override":   overrideAttribute(),
					"assets": schema.ListAttribute{
						MarkdownDescription: "Abstract asset-type paths the policy applies to (e.g., `[\"repository\"]`). Required unless `override.asset.types` is set — when only override is supplied, the provider auto-derives this list from it.",
						Optional:            true,
						ElementType:         types.StringType,
					},
				},
			},

			"pods": podsAttribute(),

			"policy_uuid": schema.StringAttribute{
				MarkdownDescription: "Computed UUID of the deployed policy entity (when `policy` is set). Tracked in state so Delete can archive the policy by UUID.",
				Computed:            true,
			},

			"pod_keys": schema.ListAttribute{
				MarkdownDescription: "Computed list of pod keys currently deployed against this gate. Used by the provider to compute add/update/delete diffs across applies.",
				Computed:            true,
				ElementType:         types.StringType,
			},
		},
	}
}

func (r *gateResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = base.EnvelopeIdentitySchema()
}

func (r *gateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *gateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan gateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if diags := r.applyPlan(ctx, &plan, nil); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&plan))...)
}

func (r *gateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state gateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// FetchGate's signature is (ctx, key, version) — no `status` param.
	fetched, err := r.client.FetchGate(ctx, state.Path.ValueString(), nil)
	if err != nil {
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch gate failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrateFromGate(ctx, &state, fetched)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&state))...)
}

func (r *gateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state gateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if diags := r.applyPlan(ctx, &plan, &state); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&plan))...)
}

func (r *gateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state gateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Detach pods first — the gate's UUID is still resolvable here and pods
	// scope by entity_id. 404s on individual pods are fine (already gone).
	if gateUUID := state.UUID.ValueString(); gateUUID != "" && !state.Detail.PodKeys.IsNull() && !state.Detail.PodKeys.IsUnknown() {
		var keys []string
		if d := state.Detail.PodKeys.ElementsAs(ctx, &keys, false); !d.HasError() {
			for _, k := range keys {
				if err := r.client.DeleteEntityPod(ctx, gateUUID, podType, k); err != nil {
					var apiErr *sdk.APIError
					if !(errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound) {
						resp.Diagnostics.AddError("delete gate pod failed", fmt.Sprintf("key=%q: %s", k, err.Error()))
						return
					}
				}
			}
		}
	}

	// Archive the attached policy next (if any) so the gate stops gating
	// deployments before it disappears.
	if uuid := state.Detail.PolicyUUID.ValueString(); uuid != "" {
		if _, err := r.client.ArchivePolicy(ctx, uuid); err != nil {
			var apiErr *sdk.APIError
			if !(errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound) {
				resp.Diagnostics.AddError("archive gate policy failed", err.Error())
				return
			}
		}
	}

	if uuid := state.UUID.ValueString(); uuid != "" {
		if _, err := r.client.ArchiveGate(ctx, uuid); err != nil {
			var apiErr *sdk.APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
				return
			}
			resp.Diagnostics.AddError("archive gate failed", err.Error())
			return
		}
	}
}

func (r *gateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("detail"), gateDetailModel{
		FullName:    types.StringNull(),
		DisplayKey:  types.StringNull(),
		Description: types.StringNull(),
		PolicyUUID:  types.StringNull(),
		PodKeys:     types.ListNull(types.StringType),
	})...)
}

// applyPlan is the shared Create/Update body. Deploys the gate, then (if
// configured) deploys the inline policy. Hydrates the plan with the
// server-returned envelope on both. Carries the prior state's UUIDs across
// when the deploy response is sparse (server returns empty EntityID on
// no-content-change "skipped" responses).
func (r *gateResource) applyPlan(ctx context.Context, plan *gateModel, prior *gateModel) diag.Diagnostics {
	var diags diag.Diagnostics

	gateResp, gateDiags := r.deployGate(ctx, *plan)
	diags.Append(gateDiags...)
	if diags.HasError() {
		return diags
	}
	diags.Append(r.hydrateAfterGateDeploy(ctx, plan, gateResp)...)
	if diags.HasError() {
		return diags
	}
	// Preserve gate UUID across partial-update responses.
	if prior != nil && (plan.UUID.IsNull() || plan.UUID.IsUnknown() || plan.UUID.ValueString() == "") {
		plan.UUID = prior.UUID
	}

	if plan.Detail.Policy != nil {
		policyUUID, policyDiags := r.deployGatePolicy(ctx, plan)
		diags.Append(policyDiags...)
		if diags.HasError() {
			return diags
		}
		plan.Detail.PolicyUUID = types.StringValue(policyUUID)
	} else {
		// If the user removed an existing inline policy, archive it.
		if prior != nil && prior.Detail.PolicyUUID.ValueString() != "" {
			if _, err := r.client.ArchivePolicy(ctx, prior.Detail.PolicyUUID.ValueString()); err != nil {
				var apiErr *sdk.APIError
				if !(errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound) {
					diags.AddError("archive removed gate policy failed", err.Error())
					return diags
				}
			}
		}
		plan.Detail.PolicyUUID = types.StringNull()
	}

	// Reconcile pods against the prior state. Strategy: upsert every pod in
	// the plan via SetEntityPod (idempotent for the same key), then delete
	// any pods that were in prior state but no longer in the plan.
	diags.Append(r.reconcilePods(ctx, plan, prior)...)
	return diags
}

// reconcilePods upserts every pod in plan.Detail.Pods on the gate, then
// deletes any pod keys that were in prior state but absent from the plan.
// Pods are per-gate, scoped server-side by (entity_id, pod_type, key).
func (r *gateResource) reconcilePods(ctx context.Context, plan *gateModel, prior *gateModel) diag.Diagnostics {
	var diags diag.Diagnostics
	gateUUID := plan.UUID.ValueString()
	if gateUUID == "" {
		// No gate UUID yet (e.g., first apply that failed to hydrate).
		// Skip pod work — the next apply will reconcile.
		plan.Detail.PodKeys = types.ListNull(types.StringType)
		return diags
	}

	desiredKeys := make(map[string]struct{}, len(plan.Detail.Pods))
	for _, p := range plan.Detail.Pods {
		pod, err := buildPod(p)
		if err != nil {
			diags.AddError("build gate pod failed", err.Error())
			return diags
		}
		if _, err := r.client.SetEntityPod(ctx, gateUUID, podType, pod.Key, pod); err != nil {
			diags.AddError("set gate pod failed", fmt.Sprintf("key=%q: %s", pod.Key, err.Error()))
			return diags
		}
		desiredKeys[pod.Key] = struct{}{}
	}

	// Delete pods that were in prior state but no longer in plan.
	if prior != nil && !prior.Detail.PodKeys.IsNull() && !prior.Detail.PodKeys.IsUnknown() {
		var priorKeys []string
		if d := prior.Detail.PodKeys.ElementsAs(ctx, &priorKeys, false); !d.HasError() {
			for _, k := range priorKeys {
				if _, keep := desiredKeys[k]; keep {
					continue
				}
				if err := r.client.DeleteEntityPod(ctx, gateUUID, podType, k); err != nil {
					var apiErr *sdk.APIError
					if !(errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound) {
						diags.AddError("delete removed gate pod failed", fmt.Sprintf("key=%q: %s", k, err.Error()))
						return diags
					}
				}
			}
		}
	}

	// Record the new key list in computed state.
	keys := make([]string, 0, len(desiredKeys))
	for k := range desiredKeys {
		keys = append(keys, k)
	}
	listVal, listDiags := types.ListValueFrom(ctx, types.StringType, keys)
	diags.Append(listDiags...)
	plan.Detail.PodKeys = listVal
	return diags
}

// deployGate marshals the gate entity and posts to
// /api/entities/artifacts/deploy with EntityType=gate.
func (r *gateResource) deployGate(ctx context.Context, plan gateModel) (*transportv1.DeployEntityFileResponse, diag.Diagnostics) {
	var diags diag.Diagnostics
	entity := buildGateEntity(plan)
	entityJSON, err := json.Marshal(entity)
	if err != nil {
		diags.AddError("marshal gate failed", err.Error())
		return nil, diags
	}
	entityTypeStr := string(db_vars.EntityTypeGateControl)
	path := plan.Path.ValueString()
	deployReq := transportv1.DeployEntityFileRequest{
		General: fianu_types.General{
			EntityType: &entityTypeStr,
			Path:       &path,
		},
	}
	deployResp, err := r.client.DeployEntityFile(ctx, deployReq, entityJSON, false)
	if err != nil {
		diags.AddError("deploy gate failed", err.Error())
		return nil, diags
	}
	return deployResp, diags
}

// deployGatePolicy deploys the nested policy as a separate entities.Policy
// targeting the gate. Returns the policy's UUID for state tracking.
func (r *gateResource) deployGatePolicy(ctx context.Context, plan *gateModel) (string, diag.Diagnostics) {
	var diags diag.Diagnostics
	entity := buildGatePolicyEntity(plan)
	entityJSON, err := json.Marshal(entity)
	if err != nil {
		diags.AddError("marshal gate policy failed", err.Error())
		return "", diags
	}
	entityTypeStr := string(db_vars.EntityTypePolicy)
	policyPath := entity.StandardEntity.Path
	deployReq := transportv1.DeployEntityFileRequest{
		General: fianu_types.General{
			EntityType: &entityTypeStr,
			Path:       &policyPath,
		},
	}
	deployResp, err := r.client.DeployEntityFile(ctx, deployReq, entityJSON, false)
	if err != nil {
		diags.AddError("deploy gate policy failed", err.Error())
		return "", diags
	}
	if deployResp != nil && deployResp.Metadata != nil && deployResp.Metadata.EntityID != "" {
		return deployResp.Metadata.EntityID, diags
	}
	// Server returned sparse response (e.g., action="skipped"). Fetch to
	// get the persisted UUID.
	fetched, err := r.client.FetchPolicy(ctx, policyPath, nil, nil)
	if err == nil && fetched != nil {
		return fetched.StandardEntity.UUID, diags
	}
	return "", diags
}

// buildGateEntity constructs a *entities.Control with Type=gate. Detail
// fields the server force-defaults (evaluation/template/relations/assets)
// are deliberately left zero — applyGateDefaults overwrites them anyway.
func buildGateEntity(plan gateModel) *fianu_entities.Control {
	c := &fianu_entities.Control{}
	c.StandardEntity.Path = plan.Path.ValueString()
	c.StandardEntity.Name = plan.Name.ValueString()
	c.StandardEntity.Type = db_vars.EntityTypeGateControl

	c.StandardEntity.Detail.Control = &fianu_entities.ControlInfo{
		FullName:    plan.Detail.FullName.ValueString(),
		DisplayKey:  plan.Detail.DisplayKey.ValueString(),
		Description: stringPtr(plan.Detail.Description),
	}
	if plan.Detail.Config != nil {
		c.StandardEntity.Detail.Config = fianu_entities.ControlConfig{
			Scope:              plan.Detail.Config.Scope.ValueString(),
			Retries:            plan.Detail.Config.Retries.ValueBool(),
			EvidenceSubmission: plan.Detail.Config.EvidenceSubmission.ValueBool(),
			ManualAttestations: plan.Detail.Config.ManualAttestations.ValueBool(),
		}
	}
	c.StandardEntity.Detail.Environments = buildEnvironments(plan.Detail.Environments)
	return c
}

// buildGatePolicyEntity constructs a *entities.Policy targeting the gate.
// Defaults: path = "<gate.path>.policy", name = gate.Name, type = "standard".
func buildGatePolicyEntity(plan *gateModel) *fianu_entities.Policy {
	gatePath := plan.Path.ValueString()
	gateName := plan.Name.ValueString()
	policy := plan.Detail.Policy

	policyPath := policy.Path.ValueString()
	if policyPath == "" {
		policyPath = gatePath + ".policy"
	}
	policyName := policy.Name.ValueString()
	if policyName == "" {
		policyName = gateName
	}
	policyType := policy.Type.ValueString()
	if policyType == "" {
		policyType = string(fianu_entities.PolicyTypeStandard)
	}

	p := &fianu_entities.Policy{}
	p.StandardEntity.Path = policyPath
	p.StandardEntity.Name = policyName
	p.StandardEntity.Type = db_vars.EntityTypePolicy

	p.StandardEntity.Detail.Type = fianu_entities.PolicyType(policyType)
	// Control.Type MUST be "gate" so the server's policy resolver queries
	// the gate table — not the control table (which is the default when
	// Type is nil). See core/pkg/policies/service.go::resolvePolicy.
	gateTypeStr := string(db_vars.EntityTypeGateControl)
	p.StandardEntity.Detail.Control = fianu_entities.PolicyControlRef{
		Path: gatePath,
		Type: &gateTypeStr,
	}
	p.StandardEntity.Detail.Variations = buildVariations(policy.Variations)
	if policy.Override != nil {
		p.StandardEntity.Detail.Override = policy.Override.toEntity()
	}

	// Detail.Assets is required by the server validator. Prefer the
	// explicit assets list; fall back to override.asset.types when only
	// override is set.
	assets := policy.Assets
	if len(assets) == 0 && policy.Override != nil {
		assets = policy.Override.Asset.Types
	}
	for _, typePath := range assets {
		if typePath.IsNull() || typePath.IsUnknown() || typePath.ValueString() == "" {
			continue
		}
		p.StandardEntity.Detail.Assets = append(p.StandardEntity.Detail.Assets, fianu_entities.PolicyAssetRef{
			Path: typePath.ValueString(),
		})
	}
	return p
}

// hydrateAfterGateDeploy refetches the gate after Create/Update so the
// response's sparse DeploymentMetadata gets supplemented with the full
// version envelope (uuid/status/state/timestamp). Falls back to
// metadata-only hydrate if the refetch fails.
func (r *gateResource) hydrateAfterGateDeploy(ctx context.Context, m *gateModel, deployResp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	fetched, err := r.client.FetchGate(ctx, m.Path.ValueString(), nil)
	if err != nil {
		return hydrateFromDeployResponse(ctx, m, deployResp)
	}
	return hydrateFromGate(ctx, m, fetched)
}

func hydrateFromDeployResponse(ctx context.Context, m *gateModel, resp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	if resp == nil || resp.Metadata == nil {
		return nil
	}
	env := base.EnvelopeFromDeployMetadata(entityType, resp.Metadata, m.Path.ValueString(), m.Name.ValueString())
	return m.Hydrate(ctx, env)
}

type identityModel struct {
	EntityType types.String `tfsdk:"entity_type"`
	EntityKey  types.String `tfsdk:"entity_key"`
	UUID       types.String `tfsdk:"uuid"`
}

func makeIdentity(m *gateModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}

// hydrateFromGate populates envelope + ControlInfo trio off the gate
// entity. Nested-policy fields stay user-authored — the policy entity is a
// separate resource fetch, and Read intentionally does NOT refetch it
// because the server canonicalises variations/override and we'd surface
// false drift. PolicyUUID is preserved from existing state.
func hydrateFromGate(ctx context.Context, m *gateModel, c *fianu_entities.Control) diag.Diagnostics {
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

func stringPtr(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	if s == "" {
		return nil
	}
	return &s
}
