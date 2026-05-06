// Package base provides the shared StandardEntity envelope (UUID, path,
// version, metadata, parents, children, roles) that every Fianu entity-style
// resource embeds. Defining the envelope once keeps schema drift in check and
// shrinks per-resource code to just the per-entity Detail fields.
package base

import (
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// EnvelopeModel is the Terraform-side representation of the envelope fields.
// Per-resource models embed this and add a typed Detail.
type EnvelopeModel struct {
	ID       types.String `tfsdk:"id"`
	UUID     types.String `tfsdk:"uuid"`
	Path     types.String `tfsdk:"path"`
	Name     types.String `tfsdk:"name"`
	Metadata types.Map    `tfsdk:"metadata"`
	Version  types.Object `tfsdk:"version"`
	Parents  types.List   `tfsdk:"parents"`
	Children types.List   `tfsdk:"children"`
}

// VersionAttrTypes mirrors the StandardEntity Version object the server
// returns. Exposed so per-resource hydrate helpers can build the typed value.
var VersionAttrTypes = map[string]any{
	"semantic":  types.StringType,
	"uuid":      types.StringType,
	"status":    types.StringType,
	"state":     types.StringType,
	"timestamp": types.StringType,
}

// EnvelopeAttributes returns the schema attributes shared across every
// entity-style resource. Per-resource Schema() methods compose this with a
// `detail` attribute carrying the entity-specific payload.
//
// Plan-modifier rules:
//   - id, uuid, version, metadata.{created_at,updated_at} → Computed +
//     UseStateForUnknown so they don't show as "(known after apply)" on
//     every plan.
//   - version.status, version.state → Computed without UseStateForUnknown
//     because workflows can mutate them server-side; users want drift to
//     surface when that happens.
//   - path → Required + RequiresReplace because the entity_key is the
//     immutable business identifier.
func EnvelopeAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{
			MarkdownDescription: "Composite resource identifier in the form `<entity_type>/<entity_key>` (e.g., `control/payment-service-sast`). Used by `terraform import`.",
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"uuid": schema.StringAttribute{
			MarkdownDescription: "Server-generated UUID. Stable across versions of the entity.",
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"path": schema.StringAttribute{
			MarkdownDescription: "Human-readable entity key (slug). Immutable — changing this forces replacement.",
			Required:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			Validators:          []validator.String{stringvalidator.LengthBetween(1, 255)},
		},
		"name": schema.StringAttribute{
			MarkdownDescription: "Display name of the entity.",
			Required:            true,
			Validators:          []validator.String{stringvalidator.LengthBetween(1, 255)},
		},
		"metadata": schema.MapAttribute{
			MarkdownDescription: "Free-form key/value metadata. Server may augment with creator/timestamps.",
			ElementType:         types.StringType,
			Optional:            true,
			Computed:            true,
		},
		"version": schema.SingleNestedAttribute{
			MarkdownDescription: "Server-managed version envelope. `status` and `state` are intentionally not pinned via UseStateForUnknown so workflow-driven changes surface as drift.",
			Computed:            true,
			Attributes: map[string]schema.Attribute{
				"semantic":  schema.StringAttribute{Computed: true, MarkdownDescription: "Semantic version (e.g., `1.0.0` or `5`)."},
				"uuid":      schema.StringAttribute{Computed: true, MarkdownDescription: "Per-version UUID. Changes whenever the server creates a new version."},
				"status":    schema.StringAttribute{Computed: true, MarkdownDescription: "Lifecycle status. May be mutated by server-side workflows."},
				"state":     schema.StringAttribute{Computed: true, MarkdownDescription: "Lifecycle state. May be mutated by server-side workflows."},
				"timestamp": schema.StringAttribute{Computed: true, MarkdownDescription: "RFC3339 timestamp the version was created."},
			},
			PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
		},
		"parents": schema.ListAttribute{
			MarkdownDescription: "Parent entity references. Server populates them from cross-entity relationships.",
			ElementType:         types.StringType,
			Optional:            true,
			Computed:            true,
		},
		"children": schema.ListAttribute{
			MarkdownDescription: "Child entity references. Server-derived; do not author manually.",
			ElementType:         types.StringType,
			Optional:            true,
			Computed:            true,
		},
	}
}
