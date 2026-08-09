// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package base

import (
	"encoding/json"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// `entities.StandardEntitySource` declares what an integration entity consumes
// and produces — the edges the server writes into entity_version_io and uses to
// validate that every consumer has a matching active producer. Tools and
// platforms both carry one, so the schema and the translation live here.

// SourcesModel is the `sources` block.
type SourcesModel struct {
	Consumes []IODefinitionModel `tfsdk:"consumes"`
	Produces []IODefinitionModel `tfsdk:"produces"`
}

// IODefinitionModel is one input or output declaration.
type IODefinitionModel struct {
	Path        string              `tfsdk:"path"`
	Note        types.String        `tfsdk:"note"`
	Integration *IOIntegrationModel `tfsdk:"integration"`
	Schema      types.String        `tfsdk:"schema"`
}

// IOIntegrationModel references the integration on the other end of the edge.
// Every field is optional on the wire; the server resolves whichever is set.
type IOIntegrationModel struct {
	Name     types.String `tfsdk:"name"`
	Type     types.String `tfsdk:"type"`
	Path     types.String `tfsdk:"path"`
	EntityID types.String `tfsdk:"entity_id"`
}

// SourcesAttribute is the shared `sources` attribute.
func SourcesAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "What this entity consumes and produces. The server records each entry as an edge and enforces that every `consumes` has a matching active `produces` somewhere, so a consumer declared against a path nothing publishes is rejected at deploy time.",
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"consumes": ioDefinitionAttribute("Inputs this entity reads."),
			"produces": ioDefinitionAttribute("Outputs this entity publishes."),
		},
	}
}

func ioDefinitionAttribute(description string) schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: description,
		Optional:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"path": schema.StringAttribute{
					MarkdownDescription: "Event or data path, e.g. `slack.message.sent`.",
					Required:            true,
				},
				"note": schema.StringAttribute{
					MarkdownDescription: "Note type this edge carries. One of `attestation`, `origin`, `occurrence`, `transaction`, `association`.",
					Optional:            true,
					Validators: []validator.String{
						stringvalidator.OneOf("attestation", "origin", "occurrence", "transaction", "association"),
					},
				},
				"integration": schema.SingleNestedAttribute{
					MarkdownDescription: "The integration on the other end. Set whichever identifier you have — the server resolves `path` or `name` to an entity, and `entity_id` pins it across renames.",
					Optional:            true,
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							MarkdownDescription: "Integration name, e.g. `slack`.",
							Optional:            true,
						},
						"type": schema.StringAttribute{
							MarkdownDescription: "Integration entity type — `platform` or `tool`.",
							Optional:            true,
							Validators: []validator.String{
								stringvalidator.OneOf("platform", "tool"),
							},
						},
						"path": schema.StringAttribute{
							MarkdownDescription: "Integration entity key. Alternative to `name`.",
							Optional:            true,
						},
						"entity_id": schema.StringAttribute{
							MarkdownDescription: "Integration entity UUID. Pins the reference across renames.",
							Optional:            true,
						},
					},
				},
				"schema": schema.StringAttribute{
					MarkdownDescription: "OpenAPI schema for the payload, as a JSON string. Author it with `jsonencode({...})` — the shape is free-form, so HCL can't type it.",
					Optional:            true,
				},
			},
		},
	}
}

// SourcesValidationDiags rejects a `schema` that isn't a JSON object at plan
// time, where the error can name the exact attribute. Without it the value is
// silently dropped in BuildSources and the entity deploys with no schema on
// that edge — a typo in a jsonencode call would look like a server-side
// omission. Callers wire this into their resource's ValidateConfig.
//
// `root` is the path to the `sources` attribute in the calling resource, since
// each entity nests it differently.
func SourcesValidationDiags(root path.Path, in *SourcesModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if in == nil {
		return diags
	}
	check := func(field string, defs []IODefinitionModel) {
		for i, d := range defs {
			s := d.Schema
			if s.IsNull() || s.IsUnknown() || s.ValueString() == "" {
				continue
			}
			var parsed map[string]any
			if err := json.Unmarshal([]byte(s.ValueString()), &parsed); err != nil {
				diags.AddAttributeError(
					root.AtName(field).AtListIndex(i).AtName("schema"),
					"schema is not a JSON object",
					"The IO schema must be a JSON object. Author it with jsonencode({...}). Parse error: "+err.Error(),
				)
			}
		}
	}
	check("consumes", in.Consumes)
	check("produces", in.Produces)
	return diags
}

// BuildSources converts the HCL model into the wire shape.
//
// Returns nil for an absent block rather than a zero-valued struct: the field
// is not `omitempty` on either ToolDetail or PlatformDetail, so an empty
// SourcesModel and an absent one must not marshal to different bytes — the
// SHA256 of these bytes is what server-side idempotency compares.
//
// A malformed `schema` string is dropped rather than surfaced. That is safe
// because it cannot happen: SourcesValidationDiags rejects it at plan time, so
// by the time this runs the string has already parsed once.
func BuildSources(in *SourcesModel) fianu_entities.StandardEntitySource {
	if in == nil {
		return fianu_entities.StandardEntitySource{
			Consumes: []fianu_entities.StandardEntityIODefinition{},
			Produces: []fianu_entities.StandardEntityIODefinition{},
		}
	}
	return fianu_entities.StandardEntitySource{
		Consumes: buildIODefinitions(in.Consumes),
		Produces: buildIODefinitions(in.Produces),
	}
}

func buildIODefinitions(in []IODefinitionModel) []fianu_entities.StandardEntityIODefinition {
	out := make([]fianu_entities.StandardEntityIODefinition, 0, len(in))
	for _, d := range in {
		def := fianu_entities.StandardEntityIODefinition{
			Path: d.Path,
			Note: d.Note.ValueString(),
		}
		if d.Integration != nil {
			def.Integration = fianu_entities.StandardEntityIOIntegration{
				Name:     optionalString(d.Integration.Name),
				Type:     optionalString(d.Integration.Type),
				Path:     optionalString(d.Integration.Path),
				EntityId: optionalString(d.Integration.EntityID),
			}
		}
		if s := d.Schema; !s.IsNull() && !s.IsUnknown() && s.ValueString() != "" {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(s.ValueString()), &parsed); err == nil {
				def.Schema = parsed
			}
		}
		out = append(out, def)
	}
	return out
}

func optionalString(v types.String) *string {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}
	s := v.ValueString()
	return &s
}
