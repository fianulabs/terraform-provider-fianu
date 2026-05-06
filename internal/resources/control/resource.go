// Package control implements the fianu_control Terraform resource.
//
// v0.1 surfaces the minimal control-authoring fields users need to write
// HCL: identity (path, name) and the ControlInfo trio (full_name, display_key,
// description). The richer ControlDetail sections (evaluation, policy
// template, relations, assets) are server-managed in v0.1 and will be added
// to the schema as we promote individual sections to first-class HCL.
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

// controlDetailModel is intentionally narrow in v0.1. Each field corresponds
// to a ControlInfo attribute on the server.
type controlDetailModel struct {
	FullName    types.String `tfsdk:"full_name"`
	DisplayKey  types.String `tfsdk:"display_key"`
	Description types.String `tfsdk:"description"`
}

func (r *controlResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_control"
}

func (r *controlResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := base.EnvelopeAttributes()
	attrs["detail"] = schema.SingleNestedAttribute{
		MarkdownDescription: "Control-specific configuration. v0.1 exposes the ControlInfo trio (`full_name`, `display_key`, `description`); future versions will add evaluation, policy template, and relations as first-class HCL.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"full_name": schema.StringAttribute{
				MarkdownDescription: "Display name of the control (e.g., `Code Coverage`).",
				Required:            true,
			},
			"display_key": schema.StringAttribute{
				MarkdownDescription: "Short uppercase key for the control (e.g., `COV`). Surfaced in dashboards and badges.",
				Required:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description of what the control validates.",
				Optional:            true,
			},
		},
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Fianu compliance control. Controls define a compliance requirement together with the rules and metadata used to evaluate it.",
		Attributes:          attrs,
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
		// Treat fetch errors as "resource gone" so terraform refresh can drop
		// the resource from state. A more nuanced error/404 split lands once
		// the SDK exposes typed not-found errors.
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

// ImportState accepts the composite `<entity_type>/<entity_key>` form (the
// canonical TF resource ID) or a bare entity_key for backward-compat with
// human-typed import commands.
func (r *controlResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key, err := base.ParseID(req.ID, entityType)
	if err != nil {
		resp.Diagnostics.AddError("invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), key)...)
}

// buildEntity translates the Terraform model into the Go-side Control entity
// the SDK marshals to JSON. Only fields the v0.1 schema exposes are set;
// server-managed Detail sub-fields (evaluation, relations, etc.) come from
// the server's authored definition and are read back via Read.
func buildEntity(plan controlModel) *fianu_entities.Control {
	var descPtr *string
	if !plan.Detail.Description.IsNull() && !plan.Detail.Description.IsUnknown() {
		if v := plan.Detail.Description.ValueString(); v != "" {
			descPtr = &v
		}
	}
	c := &fianu_entities.Control{}
	c.Name = plan.Name.ValueString()
	c.Path = plan.Path.ValueString()
	c.Type = db_vars.EntityTypeControl
	c.Detail.Control = &fianu_entities.ControlInfo{
		FullName:    plan.Detail.FullName.ValueString(),
		DisplayKey:  plan.Detail.DisplayKey.ValueString(),
		Description: descPtr,
	}
	return c
}

// hydrateFromDeployResponse seeds envelope fields from the server's deploy
// response. On action="skipped" the metadata is sparse — we fall back to
// plan-supplied path/name to keep state coherent.
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

// identityModel mirrors EnvelopeIdentitySchema. Kept private because no
// caller outside the resource needs to construct one.
type identityModel struct {
	EntityType types.String `tfsdk:"entity_type"`
	EntityKey  types.String `tfsdk:"entity_key"`
	UUID       types.String `tfsdk:"uuid"`
}

// makeIdentity builds the identity payload from the current model. Resource
// handlers feed this into resp.Identity.Set after a successful Hydrate.
func makeIdentity(m *controlModel) identityModel {
	return identityModel{
		EntityType: types.StringValue(entityType),
		EntityKey:  m.Path,
		UUID:       m.UUID,
	}
}

// hydrateFromControl pulls envelope fields off a server-side Control entity
// (returned by FetchControl). Detail fields are mapped back into the typed
// model so users see in state exactly what the server has stored.
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
