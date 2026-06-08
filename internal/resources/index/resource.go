// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package index implements the fianu_index Terraform resource.
//
// An index is a reusable asset-scope definition — one or more CEL expressions
// over an abstract asset type ("repository", "application", ...) — that
// policies and gate protected scopes reference instead of repeating the CEL
// inline. Server-side, the worker materializes index members from
// `index_compute_state` and other entities link to them via entity edges.
//
// Wire format differs from the rest of the entity ecosystem. Where
// control/gate/policy go through the generic `/api/entities/artifacts/deploy`
// multipart endpoint, indexes use a dedicated REST shape:
//
//	POST   /api/entities/indexes           — create
//	GET    /api/entities/indexes/{key}     — read (key = path OR uuid)
//	PATCH  /api/entities/indexes/{key}     — update
//	DELETE /api/entities/indexes/{key}     — archive
//
// Create/Update/Read return `*indexes.IndexWithComputeState`, which embeds
// `entities.Index` plus an operational `ComputeState` (member count, last
// recompute timestamp, etc.). Hydration reads off the embedded
// StandardEntity[IndexDetail] envelope only — the ComputeState is server-only
// and intentionally not exposed.
package index

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	sdk "github.com/fianulabs/core/v2/external/pkg/sdk/v2"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/fianulabs/terraform-provider-fianu/internal/resources/base"
)

const entityType = "index"

var (
	_ resource.Resource                = (*indexResource)(nil)
	_ resource.ResourceWithConfigure   = (*indexResource)(nil)
	_ resource.ResourceWithImportState = (*indexResource)(nil)
	_ resource.ResourceWithIdentity    = (*indexResource)(nil)
)

func NewResource() resource.Resource {
	return &indexResource{}
}

type indexResource struct {
	client *sdk.Client
}

// indexModel is the Terraform-side state.
type indexModel struct {
	base.EnvelopeModel
	Detail indexDetailModel `tfsdk:"detail"`
}

func (r *indexResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_index"
}

func (r *indexResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = detailAttribute()
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Fianu index — a reusable asset-scope definition. An index pairs an abstract asset type with one or more CEL expressions so policies and gates can reference the same scope by `id` or `path` without restating the CEL.",
		Attributes:          attrs,
	}
}

func (r *indexResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = base.EnvelopeIdentitySchema()
}

func (r *indexResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *indexResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan indexModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	entity := buildEntity(plan)
	result, err := r.client.CreateIndex(ctx, *entity)
	if err != nil {
		resp.Diagnostics.AddError("create index failed", err.Error())
		return
	}
	resp.Diagnostics.Append(hydrate(ctx, &plan, &result.Index)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&plan))...)
}

func (r *indexResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state indexModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, err := r.client.GetIndex(ctx, state.Path.ValueString(), nil)
	if err != nil {
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("fetch index failed", err.Error())
		return
	}

	resp.Diagnostics.Append(hydrate(ctx, &state, &result.Index)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&state))...)
}

func (r *indexResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan indexModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	entity := buildEntity(plan)
	result, err := r.client.UpdateIndex(ctx, plan.Path.ValueString(), *entity)
	if err != nil {
		resp.Diagnostics.AddError("update index failed", err.Error())
		return
	}
	resp.Diagnostics.Append(hydrate(ctx, &plan, &result.Index)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Identity.Set(ctx, makeIdentity(&plan))...)
}

func (r *indexResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state indexModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key := state.Path.ValueString()
	if key == "" {
		return
	}
	// ArchiveIndex accepts path or uuid via the `:key` segment; we pass the
	// path since it's what the user authored and is stable across versions.
	if err := r.client.ArchiveIndex(ctx, key); err != nil {
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return
		}
		resp.Diagnostics.AddError("archive index failed", err.Error())
		return
	}
}

func (r *indexResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
	// Pre-populate the detail object so the subsequent Read's req.State.Get
	// can decode without choking on a null nested object — indexModel.Detail
	// is a value type, not a pointer, so the framework refuses to convert
	// null into it. Mirrors the control/gate ImportState pattern. Read
	// hydrates only the envelope; user-authored Detail fields come from the
	// HCL on the next plan/apply.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("detail"), indexDetailModel{
		Description: types.StringNull(),
		AssetType:   types.StringNull(),
		CombineWith: types.StringNull(),
		Kind:        types.StringNull(),
	})...)
}

// buildEntity translates the HCL model into a wire-side Index entity.
// The server's IsValid requires path + name + either AssetType (UUID) or
// AssetTypePath (enum); we always send AssetTypePath since users author the
// enum value and the server resolves the UUID.
func buildEntity(plan indexModel) *fianu_entities.Index {
	e := fianu_entities.NewIndex()
	e.Path = plan.Path.ValueString()
	e.Name = plan.Name.ValueString()
	applyDetail(e, plan.Detail)
	return e
}

// hydrate populates envelope state from the canonical Index entity. Follows
// the Hydration Rule: envelope-only, plus the minimal set of user-authored
// identity fields that are also `RequiresReplace`. For control that's the
// `ControlInfo` trio (full_name/display_key/description); for index that's
// `asset_type` only.
//
// Why asset_type is hydrated: it's `RequiresReplace`, so a post-import plan
// that compares config "repository" against state null would force
// destroy+create, defeating the point of import. Hydrating from
// AssetTypePath keeps the post-import workflow usable AND surfaces
// server-side mutation of asset_type as drift (useful — a worker bug or
// admin override would otherwise be invisible).
//
// Other Detail fields (description, combine_with, kind,
// dependent_asset_types, expressions) stay user-authored — server-side
// canonicalisation would otherwise show false drift. ComputeState (member
// count, recompute timestamps) is server-only and intentionally not
// surfaced since it changes independently of user intent.
func hydrate(ctx context.Context, m *indexModel, e *fianu_entities.Index) diag.Diagnostics {
	if e == nil {
		return nil
	}
	env := base.EnvelopeFromStandardEntity(entityType, &e.StandardEntity)
	diags := m.Hydrate(ctx, env)
	if e.Detail.AssetTypePath != "" {
		m.Detail.AssetType = types.StringValue(e.Detail.AssetTypePath)
	}
	return diags
}

type identityModel struct {
	EntityType types.String `tfsdk:"entity_type"`
	EntityKey  types.String `tfsdk:"entity_key"`
	UUID       types.String `tfsdk:"uuid"`
}

func makeIdentity(m *indexModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}
