// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package base

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// `entities.Documentation` is a title+URL pair that hangs off the detail of
// most entity types (environment, target, collection, control). Defined once
// here so each resource composes it instead of restating four lines of schema.

// DocModel is one documentation link.
type DocModel struct {
	Title types.String `tfsdk:"title"`
	URL   types.String `tfsdk:"url"`
}

// DocumentationAttribute is the shared `documentation` list attribute.
func DocumentationAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "Documentation links surfaced alongside the entity in the Console.",
		Optional:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"title": schema.StringAttribute{
					MarkdownDescription: "Link text.",
					Required:            true,
				},
				"url": schema.StringAttribute{
					MarkdownDescription: "Link target.",
					Required:            true,
				},
			},
		},
	}
}

// BuildDocumentation converts the HCL models into the wire shape.
//
// Returns an empty (non-nil) slice rather than nil so the marshalled output is
// deterministic for a given config, which is what the SHA256 content hash
// behind server-side idempotency depends on. Whether that empty slice reaches
// the wire varies by entity and does not matter here: EnvironmentDetail tags
// the field `omitempty` so `[]` is dropped, CollectionDetail does not so it is
// sent — either way the same config always produces the same bytes.
func BuildDocumentation(in []DocModel) []fianu_entities.Documentation {
	out := make([]fianu_entities.Documentation, 0, len(in))
	for _, d := range in {
		out = append(out, fianu_entities.Documentation{
			Title: d.Title.ValueString(),
			URL:   d.URL.ValueString(),
		})
	}
	return out
}
