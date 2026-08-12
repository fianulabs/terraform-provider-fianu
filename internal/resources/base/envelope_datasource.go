// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package base

import (
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// EnvelopeDataSourceAttributes is the read-only twin of EnvelopeAttributes.
//
// It exists as a separate function rather than a shared one because the
// framework gives resources and data sources distinct schema packages
// (`resource/schema` vs `datasource/schema`) with distinct attribute
// interfaces. There is no type the two have in common to build once and
// convert, so the field set is restated here.
//
// The differences from the resource schema are the whole point:
//
//   - `path` is Required — it is the lookup key, not a managed value.
//   - Everything else is Computed. A data source never plans a change, so
//     Optional would be meaningless and plan modifiers do not apply.
//   - `name` moves from Required to Computed: on a resource the user authors
//     it, here the server is the only source.
//
// EnvelopeModel is shared between the two — only the schema differs, so
// Hydrate works unchanged.
func EnvelopeDataSourceAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{
			MarkdownDescription: "Composite identifier in the form `<entity_type>/<entity_key>` (e.g., `platform/f.platform.jira`).",
			Computed:            true,
		},
		"uuid": schema.StringAttribute{
			MarkdownDescription: "Server-generated UUID, stable across versions of the entity. This is the value other resources reference — for example `fianu_instance`'s `detail.platform_uuid`.",
			Computed:            true,
		},
		"path": schema.StringAttribute{
			MarkdownDescription: "Entity key (slug) to look up, e.g. `f.platform.jira`. Lookup is by path rather than UUID because the path is the stable, human-authored identifier — the same key `fianu console deploy` writes.",
			Required:            true,
			Validators:          []validator.String{stringvalidator.LengthBetween(1, 255)},
		},
		"name": schema.StringAttribute{
			MarkdownDescription: "Display name of the entity.",
			Computed:            true,
		},
		"metadata": schema.MapAttribute{
			MarkdownDescription: "Free-form key/value metadata as stored server-side.",
			ElementType:         types.StringType,
			Computed:            true,
		},
		"version": schema.SingleNestedAttribute{
			MarkdownDescription: "Version envelope of the entity as currently published.",
			Computed:            true,
			Attributes: map[string]schema.Attribute{
				"semantic":  schema.StringAttribute{Computed: true, MarkdownDescription: "Semantic version (e.g., `1.0.0` or `5`)."},
				"uuid":      schema.StringAttribute{Computed: true, MarkdownDescription: "Per-version UUID."},
				"status":    schema.StringAttribute{Computed: true, MarkdownDescription: "Lifecycle status."},
				"state":     schema.StringAttribute{Computed: true, MarkdownDescription: "Lifecycle state."},
				"timestamp": schema.StringAttribute{Computed: true, MarkdownDescription: "RFC3339 timestamp the version was created."},
			},
		},
		"parents": schema.ListAttribute{
			MarkdownDescription: "Parent entity references.",
			ElementType:         types.StringType,
			Computed:            true,
		},
		"children": schema.ListAttribute{
			MarkdownDescription: "Child entity references.",
			ElementType:         types.StringType,
			Computed:            true,
		},
	}
}
