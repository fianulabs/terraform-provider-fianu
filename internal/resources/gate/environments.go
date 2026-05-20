// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package gate

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// environmentRefModel mirrors fianu_entities.StandardEntityReference for the
// gate-only `environments` field (entity-edge bindings to environment
// entities). Either `path` or `uuid` is sufficient — the server resolves
// whichever is supplied at deploy time.
type environmentRefModel struct {
	Path types.String `tfsdk:"path"`
	UUID types.String `tfsdk:"uuid"`
	Name types.String `tfsdk:"name"`
}

func environmentsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "Environment entities this gate binds to. Maps to the `environments` edge on the gate entity — server materialises these as `parent=environment, child=gate` entity_edges rows. Gate-only field (controls don't bind to environments).",
		Optional:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"path": schema.StringAttribute{
					MarkdownDescription: "Environment entity path (e.g., `env.prod`). At least one of `path` or `uuid` is required.",
					Optional:            true,
				},
				"uuid": schema.StringAttribute{
					MarkdownDescription: "Environment entity UUID. Pin this when binding across path renames.",
					Optional:            true,
				},
				"name": schema.StringAttribute{
					MarkdownDescription: "Optional human-readable name. Server-side ignored for resolution — purely for HCL readability.",
					Optional:            true,
				},
			},
		},
	}
}

// buildEnvironments translates HCL-side environment refs into the wire shape.
// Empty/nil input maps to a nil slice (server's `omitempty` drops the key
// entirely, which matches a control's behaviour).
func buildEnvironments(in []environmentRefModel) []fianu_entities.StandardEntityReference {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.StandardEntityReference, len(in))
	for i, e := range in {
		ref := fianu_entities.StandardEntityReference{
			Type: db_vars.EntityTypeEnvironment,
		}
		if v := e.UUID; !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
			ref.UUID = v.ValueString()
		}
		if v := e.Path; !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
			s := v.ValueString()
			ref.Path = &s
		}
		if v := e.Name; !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
			s := v.ValueString()
			ref.Name = &s
		}
		out[i] = ref
	}
	return out
}
