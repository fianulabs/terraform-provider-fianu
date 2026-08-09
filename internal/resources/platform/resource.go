// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package platform implements the fianu_platform Terraform resource.
//
// A platform is the integration *product* — GitHub, Jira, Slack — that
// instances connect to. It carries the shared operational contract every
// instance inherits: base URL, health probes, credential rotation, error
// semantics and audit policy.
//
// Wire shape:
//
//	name: ...
//	path: ...
//	type: platform
//	detail:
//	  description: ...
//	  platformType: { name: ..., uuid: ... }
//	  toolVersion: ...
//	  sources: { produces: [...], consumes: [...] }
//	  endpointDefaults: { baseUrl: ..., defaultHeaders: {...} }
//	  healthChecks: [...]
//	  credentialPolicy: {...}
//	  errorMappings: [...]
//	  auditPolicy: {...}
//
// Two PlatformDetail fields are deliberately absent from the schema:
//
//   - `capabilities` is the Fianu-owned capability catalog shipped per platform
//     version, not customer configuration.
//   - `instances` is a computed count of the instances attached to the
//     platform. Accepting it from HCL would let a config assert a number the
//     server derives.
//
// `entities.Platform` embeds `StandardEntity[PlatformDetail]`, so Detail is
// reached through the embedded field.
package platform

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

const entityType = "platform"

// Compile-time interface checks.
var (
	_ resource.Resource                   = (*platformResource)(nil)
	_ resource.ResourceWithConfigure      = (*platformResource)(nil)
	_ resource.ResourceWithImportState    = (*platformResource)(nil)
	_ resource.ResourceWithIdentity       = (*platformResource)(nil)
	_ resource.ResourceWithValidateConfig = (*platformResource)(nil)
)

// NewResource is the factory the provider package registers.
func NewResource() resource.Resource {
	return &platformResource{}
}

type platformResource struct {
	client *sdk.Client
}

type platformModel struct {
	base.EnvelopeModel
	Detail platformDetailModel `tfsdk:"detail"`
}

type platformDetailModel struct {
	Description      types.String           `tfsdk:"description"`
	PlatformType     *platformTypeModel     `tfsdk:"platform_type"`
	DisplayLogo      types.String           `tfsdk:"display_logo"`
	WebsiteURL       types.String           `tfsdk:"website_url"`
	DocsURL          types.String           `tfsdk:"docs_url"`
	LogoURL          types.String           `tfsdk:"logo_url"`
	Features         map[string]string      `tfsdk:"features"`
	ToolVersion      types.String           `tfsdk:"tool_version"`
	Sources          *base.SourcesModel     `tfsdk:"sources"`
	EndpointDefaults *endpointDefaultsModel `tfsdk:"endpoint_defaults"`
	HealthChecks     []healthCheckModel     `tfsdk:"health_checks"`
	CredentialPolicy *credentialPolicyModel `tfsdk:"credential_policy"`
	ErrorMappings    []errorMappingModel    `tfsdk:"error_mappings"`
	AuditPolicy      *auditPolicyModel      `tfsdk:"audit_policy"`
}

type platformTypeModel struct {
	Name types.String `tfsdk:"name"`
	UUID types.String `tfsdk:"uuid"`
}

func (r *platformResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_platform"
}

func (r *platformResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = detailAttribute()
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Fianu platform — the integration product (GitHub, Jira, Slack) that instances connect to. The platform carries the operational contract every instance of it inherits: endpoint defaults, health probes, credential rotation, error semantics and audit policy.",
		Attributes:          attrs,
	}
}

func detailAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Platform payload — mirrors the spec.yaml structure used by `fianu console deploy`.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description of the platform.",
				Optional:            true,
			},
			"platform_type": schema.SingleNestedAttribute{
				MarkdownDescription: "The platform type this platform belongs to. Platform types are Console-managed — there is no `fianu_platform_type` resource yet — so supply the `uuid` of an existing one, the same way `fianu_collection` references a domain.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						MarkdownDescription: "Display name of the platform type.",
						Optional:            true,
					},
					"uuid": schema.StringAttribute{
						MarkdownDescription: "Entity UUID of the platform type.",
						Optional:            true,
					},
				},
			},
			"display_logo": schema.StringAttribute{
				MarkdownDescription: "Logo identifier used by the Console's own icon set.",
				Optional:            true,
			},
			"website_url": schema.StringAttribute{
				MarkdownDescription: "Vendor website.",
				Optional:            true,
			},
			"docs_url": schema.StringAttribute{
				MarkdownDescription: "Vendor documentation.",
				Optional:            true,
			},
			"logo_url": schema.StringAttribute{
				MarkdownDescription: "Externally hosted logo image.",
				Optional:            true,
			},
			"features": schema.MapAttribute{
				MarkdownDescription: "Free-form feature flags for this platform, as string key/value pairs.",
				Optional:            true,
				ElementType:         types.StringType,
			},
			"tool_version": schema.StringAttribute{
				MarkdownDescription: "Version of the platform integration itself (not the entity version).",
				Optional:            true,
			},
			"sources":           base.SourcesAttribute(),
			"endpoint_defaults": endpointDefaultsAttribute(),
			"health_checks":     healthChecksAttribute(),
			"credential_policy": credentialPolicyAttribute(),
			"error_mappings":    errorMappingsAttribute(),
			"audit_policy":      auditPolicyAttribute(),
		},
	}
}

func (r *platformResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = base.EnvelopeIdentitySchema()
}

func (r *platformResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ValidateConfig catches malformed jsonencode-authored attributes at plan
// time, where the diagnostic can name the attribute, instead of letting them
// deploy as empty columns.
func (r *platformResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg platformModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateJSONObjectAttributes(cfg.Detail)...)
	resp.Diagnostics.Append(base.SourcesValidationDiags(path.Root("detail").AtName("sources"), cfg.Detail.Sources)...)
}

func (r *platformResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan platformModel
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

func (r *platformResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state platformModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fetched, err := r.client.FetchPlatform(ctx, state.Path.ValueString())
	if err != nil {
		// Only a real 404 evicts state. Other errors (network, 5xx, transient
		// auth) surface as a diagnostic so apply doesn't silently drop a
		// resource that still exists server-side.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch platform failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrateFromPlatform(ctx, &state, fetched)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&state))...)
}

func (r *platformResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan platformModel
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

func (r *platformResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state platformModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	uuid := state.UUID.ValueString()
	if uuid == "" {
		return
	}
	if _, err := r.client.ArchivePlatform(ctx, uuid); err != nil {
		// 404 means it's already gone — happy path for destroy.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("archive platform failed", err.Error())
	}
}

func (r *platformResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
	// Pre-populate detail so the post-import Read can decode: platformModel
	// .Detail is a value type and the framework refuses to convert null into
	// it. Same fix as control, policy and tool.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("detail"), platformDetailModel{
		Description: types.StringNull(),
		DisplayLogo: types.StringNull(),
		WebsiteURL:  types.StringNull(),
		DocsURL:     types.StringNull(),
		LogoURL:     types.StringNull(),
		ToolVersion: types.StringNull(),
	})...)
}

// applyPlan is the shared Create/Update body: deploy, then refetch so the
// sparse DeploymentMetadata gets supplemented with the full version envelope.
func (r *platformResource) applyPlan(ctx context.Context, plan *platformModel) diag.Diagnostics {
	deployResp, diags := r.deploy(ctx, *plan)
	if diags.HasError() {
		return diags
	}
	fetched, err := r.client.FetchPlatform(ctx, plan.Path.ValueString())
	if err != nil {
		return hydrateFromDeployResponse(ctx, plan, deployResp)
	}
	return hydrateFromPlatform(ctx, plan, fetched)
}

func (r *platformResource) deploy(ctx context.Context, plan platformModel) (*transportv1.DeployEntityFileResponse, diag.Diagnostics) {
	var diags diag.Diagnostics
	entityJSON, err := json.Marshal(buildEntity(plan))
	if err != nil {
		diags.AddError("marshal platform failed", err.Error())
		return nil, diags
	}
	entityTypeStr := string(db_vars.EntityTypePlatform)
	entityPath := plan.Path.ValueString()
	deployResp, err := r.client.DeployEntityFile(ctx, transportv1.DeployEntityFileRequest{
		General: fianu.General{EntityType: &entityTypeStr, Path: &entityPath},
	}, entityJSON, false)
	if err != nil {
		diags.AddError("deploy platform failed", err.Error())
		return nil, diags
	}
	return deployResp, diags
}

// buildEntity translates the HCL model into the wire entity.
func buildEntity(plan platformModel) *fianu_entities.Platform {
	p := &fianu_entities.Platform{}
	p.Path = plan.Path.ValueString()
	p.Name = plan.Name.ValueString()
	p.Type = db_vars.EntityTypePlatform

	d := plan.Detail
	p.Detail.Description = d.Description.ValueString()
	p.Detail.DisplayLogo = d.DisplayLogo.ValueString()
	p.Detail.WebsiteURL = d.WebsiteURL.ValueString()
	p.Detail.DocsURL = d.DocsURL.ValueString()
	p.Detail.LogoURL = d.LogoURL.ValueString()
	p.Detail.ToolVersion = d.ToolVersion.ValueString()
	p.Detail.Features = d.Features
	if d.PlatformType != nil {
		p.Detail.PlatformType = fianu_entities.PlatformType{
			Name: d.PlatformType.Name.ValueString(),
			UUID: d.PlatformType.UUID.ValueString(),
		}
	}
	p.Detail.Sources = base.BuildSources(d.Sources)
	p.Detail.EndpointDefaults = buildEndpointDefaults(d.EndpointDefaults)
	p.Detail.HealthChecks = buildHealthChecks(d.HealthChecks)
	p.Detail.CredentialPolicy = buildCredentialPolicy(d.CredentialPolicy)
	p.Detail.ErrorMappings = buildErrorMappings(d.ErrorMappings)
	p.Detail.AuditPolicy = buildAuditPolicy(d.AuditPolicy)
	return p
}

func hydrateFromDeployResponse(ctx context.Context, m *platformModel, resp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	if resp == nil || resp.Metadata == nil {
		return nil
	}
	env := base.EnvelopeFromDeployMetadata(entityType, resp.Metadata, m.Path.ValueString(), m.Name.ValueString())
	return m.Hydrate(ctx, env)
}

// hydrateFromPlatform populates envelope state only. Detail stays
// user-authored: the server stamps row IDs and platformVersionUuid onto every
// sub-section it persists, and those fields aren't in the schema, so hydrating
// Detail would drift on every plan. Same rule as control, policy, environment
// and tool.
func hydrateFromPlatform(ctx context.Context, m *platformModel, p *fianu_entities.Platform) diag.Diagnostics {
	if p == nil {
		return nil
	}
	return m.Hydrate(ctx, base.EnvelopeFromStandardEntity(entityType, &p.StandardEntity))
}

type identityModel struct {
	EntityType types.String `tfsdk:"entity_type"`
	EntityKey  types.String `tfsdk:"entity_key"`
	UUID       types.String `tfsdk:"uuid"`
}

func makeIdentity(m *platformModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}
