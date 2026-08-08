// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package target implements the fianu_target Terraform resource.
//
// A target is a concrete cloud deployment destination — an EKS cluster, a
// Cloud Run service, a Lambda region — bound to one or more environments.
// Gates evaluate deployment events against the target's environments.
//
// Wire shape:
//
//	name: ...
//	path: ...
//	type: target
//	detail:
//	  description: ...
//	  cloudProvider: AWS
//	  type: kubernetes
//	  service: ...
//	  region: us-east-1
//	  solution: ...
//	  tags: [...]
//	environments: [{ environment: <path or uuid> }]
//	aliases: [...]
//	documentation: [{ title: ..., url: ... }]
//
// Note the last three are **siblings of detail**, not children: the wire type
// is `entities.TargetWithEnvironment`, which embeds `Target`
// (StandardEntity[TargetDetail]) and hangs the satellites off the top level.
// It carries a custom UnmarshalJSON precisely because StandardEntity's own
// unmarshal would otherwise consume the whole blob and drop them.
//
// `environments` accepts a path or a UUID — the server resolves paths via its
// key resolver at deploy time, the same as `control.relations`. At least one
// is required (`TargetWithEnvironment.IsValid`).
package target

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
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/fianulabs/terraform-provider-fianu/internal/resources/base"
)

const entityType = "target"

// Compile-time interface checks.
var (
	_ resource.Resource                = (*targetResource)(nil)
	_ resource.ResourceWithConfigure   = (*targetResource)(nil)
	_ resource.ResourceWithImportState = (*targetResource)(nil)
	_ resource.ResourceWithIdentity    = (*targetResource)(nil)
)

// NewResource is the factory the provider package registers.
func NewResource() resource.Resource {
	return &targetResource{}
}

type targetResource struct {
	client *sdk.Client
}

type targetModel struct {
	base.EnvelopeModel
	Detail targetDetailModel `tfsdk:"detail"`

	// Environments, Aliases and Documentation are satellites of the target
	// entity, not part of TargetDetail — they live at the top level of the
	// wire payload. The HCL surface mirrors that so the mapping stays obvious.
	Environments  []environmentRefModel `tfsdk:"environments"`
	Aliases       []types.String        `tfsdk:"aliases"`
	Documentation []base.DocModel       `tfsdk:"documentation"`
}

type targetDetailModel struct {
	Description   types.String   `tfsdk:"description"`
	CloudProvider types.String   `tfsdk:"cloud_provider"`
	Type          types.String   `tfsdk:"type"`
	Service       types.String   `tfsdk:"service"`
	Region        types.String   `tfsdk:"region"`
	Solution      types.String   `tfsdk:"solution"`
	Tags          []types.String `tfsdk:"tags"`
}

type environmentRefModel struct {
	Environment types.String `tfsdk:"environment"`
}

func (r *targetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_target"
}

func (r *targetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = detailAttribute()
	attrs["environments"] = environmentsAttribute()
	attrs["aliases"] = schema.ListAttribute{
		MarkdownDescription: "Alternative names this target is known by. Useful when a deployment pipeline reports a different identifier than the target's `name`.",
		Optional:            true,
		ElementType:         types.StringType,
	}
	attrs["documentation"] = base.DocumentationAttribute()

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Fianu deployment target — a concrete cloud destination (EKS cluster, Cloud Run service, Lambda region) bound to one or more environments. Gates evaluate deployment events against a target's environments.",
		Attributes:          attrs,
	}
}

func detailAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Target payload — what and where this deployment destination is.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description of the target.",
				Optional:            true,
			},
			"cloud_provider": schema.StringAttribute{
				MarkdownDescription: "Cloud provider, e.g. `AWS`, `GCP`, `Azure`.",
				Optional:            true,
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "Classification of the target, e.g. `kubernetes`, `serverless`.",
				Optional:            true,
			},
			"service": schema.StringAttribute{
				MarkdownDescription: "Cloud service name, e.g. `eks`, `cloudrun`, `lambda`.",
				Optional:            true,
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Cloud region, e.g. `us-east-1`.",
				Optional:            true,
			},
			"solution": schema.StringAttribute{
				MarkdownDescription: "Solution identifier this target belongs to.",
				Optional:            true,
			},
			"tags": schema.ListAttribute{
				MarkdownDescription: "Free-form tags.",
				Optional:            true,
				ElementType:         types.StringType,
			},
		},
	}
}

func environmentsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "Environments this target deploys into. At least one is required — the server rejects a target with none. Each entry takes an environment path or UUID; paths are resolved server-side at deploy time.",
		Required:            true,
		Validators: []validator.List{
			listvalidator.SizeAtLeast(1),
		},
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"environment": schema.StringAttribute{
					MarkdownDescription: "Environment path (e.g. from `fianu_environment.production.path`) or UUID.",
					Required:            true,
				},
			},
		},
	}
}

func (r *targetResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = base.EnvelopeIdentitySchema()
}

func (r *targetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *targetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan targetModel
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

func (r *targetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state targetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fetched, err := r.client.FetchTarget(ctx, state.Path.ValueString(), nil)
	if err != nil {
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch target failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrateFromTarget(ctx, &state, fetched)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&state))...)
}

func (r *targetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan targetModel
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

func (r *targetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state targetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	uuid := state.UUID.ValueString()
	if uuid == "" {
		return
	}
	if _, err := r.client.ArchiveTarget(ctx, uuid); err != nil {
		// 404 means it's already gone — happy path for destroy.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("archive target failed", err.Error())
	}
}

func (r *targetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
}

func (r *targetResource) applyPlan(ctx context.Context, plan *targetModel) diag.Diagnostics {
	deployResp, diags := r.deploy(ctx, *plan)
	if diags.HasError() {
		return diags
	}
	fetched, err := r.client.FetchTarget(ctx, plan.Path.ValueString(), nil)
	if err != nil {
		return hydrateFromDeployResponse(ctx, plan, deployResp)
	}
	return hydrateFromTarget(ctx, plan, fetched)
}

func (r *targetResource) deploy(ctx context.Context, plan targetModel) (*transportv1.DeployEntityFileResponse, diag.Diagnostics) {
	var diags diag.Diagnostics
	entityJSON, err := json.Marshal(buildEntity(plan))
	if err != nil {
		diags.AddError("marshal target failed", err.Error())
		return nil, diags
	}
	entityTypeStr := string(db_vars.EntityTypeTarget)
	entityPath := plan.Path.ValueString()
	deployResp, err := r.client.DeployEntityFile(ctx, transportv1.DeployEntityFileRequest{
		General: fianu.General{EntityType: &entityTypeStr, Path: &entityPath},
	}, entityJSON, false)
	if err != nil {
		diags.AddError("deploy target failed", err.Error())
		return nil, diags
	}
	return deployResp, diags
}

// buildEntity translates the HCL model into the wire entity.
//
// TargetWithEnvironment embeds Target, which wraps StandardEntity — so
// envelope and Detail writes go through two levels of promotion
// (t.Target.StandardEntity). The satellites (Environments/Aliases/
// Documentation) sit on the outer struct. Writing Detail via the promoted
// path is safe here: unlike entities.Policy there is no second inlined copy
// of TargetDetail to shadow it.
func buildEntity(plan targetModel) *fianu_entities.TargetWithEnvironment {
	t := &fianu_entities.TargetWithEnvironment{}
	t.Path = plan.Path.ValueString()
	t.Name = plan.Name.ValueString()
	t.Type = db_vars.EntityTypeTarget

	t.Detail.Description = plan.Detail.Description.ValueString()
	t.Detail.CloudProvider = plan.Detail.CloudProvider.ValueString()
	t.Detail.TargetType = plan.Detail.Type.ValueString()
	t.Detail.Service = plan.Detail.Service.ValueString()
	t.Detail.Region = plan.Detail.Region.ValueString()
	t.Detail.Solution = plan.Detail.Solution.ValueString()
	t.Detail.Tags = stringsOf(plan.Detail.Tags)

	t.Environments = make([]fianu_entities.TargetToEnvironmentRef, 0, len(plan.Environments))
	for _, e := range plan.Environments {
		// Name is left empty: the server resolves the ref (path or UUID) and
		// fills the display name on read.
		t.Environments = append(t.Environments, fianu_entities.TargetToEnvironmentRef{
			Environment: e.Environment.ValueString(),
		})
	}
	t.Aliases = stringsOf(plan.Aliases)
	t.Documentation = base.BuildDocumentation(plan.Documentation)
	return t
}

func stringsOf(in []types.String) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v.IsNull() || v.IsUnknown() {
			continue
		}
		out = append(out, v.ValueString())
	}
	return out
}

func hydrateFromDeployResponse(ctx context.Context, m *targetModel, resp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	if resp == nil || resp.Metadata == nil {
		return nil
	}
	env := base.EnvelopeFromDeployMetadata(entityType, resp.Metadata, m.Path.ValueString(), m.Name.ValueString())
	return m.Hydrate(ctx, env)
}

// hydrateFromTarget populates envelope state only.
//
// Detail and the satellites stay user-authored. `environments` in particular
// MUST NOT be hydrated: the user writes environment paths, the server resolves
// them to UUIDs and adds display names, so echoing the response back would
// rewrite every path into a UUID and show a permanent diff.
func hydrateFromTarget(ctx context.Context, m *targetModel, t *fianu_entities.TargetWithEnvironment) diag.Diagnostics {
	if t == nil {
		return nil
	}
	// t.StandardEntity resolves through TargetWithEnvironment -> Target ->
	// StandardEntity. Two levels of embedding, one unambiguous promotion —
	// unlike entities.Policy there is no second inlined copy to shadow it.
	return m.Hydrate(ctx, base.EnvelopeFromStandardEntity(entityType, &t.StandardEntity))
}

type identityModel struct {
	EntityType types.String `tfsdk:"entity_type"`
	EntityKey  types.String `tfsdk:"entity_key"`
	UUID       types.String `tfsdk:"uuid"`
}

func makeIdentity(m *targetModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}
