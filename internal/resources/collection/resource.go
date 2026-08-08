// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package collection implements the fianu_collection Terraform resource.
//
// A collection groups controls under a domain — the middle tier of the
// hierarchy (domain → collection → control). It is the smallest entity in the
// provider: identity plus a domain reference.
//
// Wire shape:
//
//	name: ...
//	path: ...
//	type: collection
//	detail:
//	  description: ...
//	  domain: <domain entity UUID>
//	  inheritDomainPermissions: true
//	  documentation: [{ title: ..., url: ... }]
//
// `detail.domain` is the parent domain's **entity UUID**, not a path — the
// server's CollectionDetail.Validate rejects an empty one and does no path
// resolution of its own. Domains are not yet a provider resource, so the UUID
// comes from the Console.
package collection

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

const entityType = "collection"

// Compile-time interface checks.
var (
	_ resource.Resource                = (*collectionResource)(nil)
	_ resource.ResourceWithConfigure   = (*collectionResource)(nil)
	_ resource.ResourceWithImportState = (*collectionResource)(nil)
	_ resource.ResourceWithIdentity    = (*collectionResource)(nil)
)

// NewResource is the factory the provider package registers.
func NewResource() resource.Resource {
	return &collectionResource{}
}

type collectionResource struct {
	client *sdk.Client
}

type collectionModel struct {
	base.EnvelopeModel
	Detail collectionDetailModel `tfsdk:"detail"`
}

type collectionDetailModel struct {
	Description              types.String    `tfsdk:"description"`
	Domain                   types.String    `tfsdk:"domain"`
	InheritDomainPermissions types.Bool      `tfsdk:"inherit_domain_permissions"`
	Documentation            []base.DocModel `tfsdk:"documentation"`
}

func (r *collectionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_collection"
}

func (r *collectionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = detailAttribute()
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Fianu collection — the grouping of controls under a domain (domain → collection → control).",
		Attributes:          attrs,
	}
}

func detailAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Collection payload.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description of what this collection groups.",
				Optional:            true,
			},
			"domain": schema.StringAttribute{
				MarkdownDescription: "Entity **UUID** of the parent domain — not a path. The server does no path resolution for this field and rejects an empty value. Domains are managed in the Console; copy the UUID from there.",
				Required:            true,
			},
			"inherit_domain_permissions": schema.BoolAttribute{
				MarkdownDescription: "Whether this collection inherits its access permissions from the parent domain. Defaults to `false`.",
				Optional:            true,
			},
			"documentation": base.DocumentationAttribute(),
		},
	}
}

func (r *collectionResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = base.EnvelopeIdentitySchema()
}

func (r *collectionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *collectionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan collectionModel
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

func (r *collectionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state collectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fetched, err := r.client.FetchCollection(ctx, state.Path.ValueString(), nil)
	if err != nil {
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch collection failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrateFromCollection(ctx, &state, fetched)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&state))...)
}

func (r *collectionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan collectionModel
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

func (r *collectionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state collectionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	uuid := state.UUID.ValueString()
	if uuid == "" {
		return
	}
	if _, err := r.client.ArchiveCollection(ctx, uuid); err != nil {
		// 404 means it's already gone — happy path for destroy.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("archive collection failed", err.Error())
	}
}

func (r *collectionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
}

func (r *collectionResource) applyPlan(ctx context.Context, plan *collectionModel) diag.Diagnostics {
	deployResp, diags := r.deploy(ctx, *plan)
	if diags.HasError() {
		return diags
	}
	fetched, err := r.client.FetchCollection(ctx, plan.Path.ValueString(), nil)
	if err != nil {
		return hydrateFromDeployResponse(ctx, plan, deployResp)
	}
	return hydrateFromCollection(ctx, plan, fetched)
}

func (r *collectionResource) deploy(ctx context.Context, plan collectionModel) (*transportv1.DeployEntityFileResponse, diag.Diagnostics) {
	var diags diag.Diagnostics
	entityJSON, err := json.Marshal(buildEntity(plan))
	if err != nil {
		diags.AddError("marshal collection failed", err.Error())
		return nil, diags
	}
	entityTypeStr := string(db_vars.EntityTypeCollection)
	entityPath := plan.Path.ValueString()
	deployResp, err := r.client.DeployEntityFile(ctx, transportv1.DeployEntityFileRequest{
		General: fianu.General{EntityType: &entityTypeStr, Path: &entityPath},
	}, entityJSON, false)
	if err != nil {
		diags.AddError("deploy collection failed", err.Error())
		return nil, diags
	}
	return deployResp, diags
}

// buildEntity translates the HCL model into the wire entity. Collection wraps
// (rather than aliases) StandardEntity, so envelope fields go through the
// embedded promotion and Detail is still unambiguous — there is no second
// inlined copy of CollectionDetail to write to by mistake.
func buildEntity(plan collectionModel) *fianu_entities.Collection {
	c := &fianu_entities.Collection{}
	c.Path = plan.Path.ValueString()
	c.Name = plan.Name.ValueString()
	c.Type = db_vars.EntityTypeCollection

	c.Detail.Description = plan.Detail.Description.ValueString()
	c.Detail.Domain = plan.Detail.Domain.ValueString()
	c.Detail.InheritDomainPermissions = plan.Detail.InheritDomainPermissions.ValueBool()
	c.Detail.Documentation = base.BuildDocumentation(plan.Detail.Documentation)
	return c
}

func hydrateFromDeployResponse(ctx context.Context, m *collectionModel, resp *transportv1.DeployEntityFileResponse) diag.Diagnostics {
	if resp == nil || resp.Metadata == nil {
		return nil
	}
	env := base.EnvelopeFromDeployMetadata(entityType, resp.Metadata, m.Path.ValueString(), m.Name.ValueString())
	return m.Hydrate(ctx, env)
}

// hydrateFromCollection populates envelope state only. Detail stays
// user-authored: the server populates `domainName` on read and normalises
// `documentation`, so hydrating Detail would surface drift the user cannot
// resolve from HCL.
func hydrateFromCollection(ctx context.Context, m *collectionModel, c *fianu_entities.Collection) diag.Diagnostics {
	if c == nil {
		return nil
	}
	return m.Hydrate(ctx, base.EnvelopeFromStandardEntity(entityType, &c.StandardEntity))
}

type identityModel struct {
	EntityType types.String `tfsdk:"entity_type"`
	EntityKey  types.String `tfsdk:"entity_key"`
	UUID       types.String `tfsdk:"uuid"`
}

func makeIdentity(m *collectionModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}
