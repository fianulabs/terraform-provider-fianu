// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package control

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// relationModel is one entry in detail.relations. The collection/domain
// fields accept either UUIDs or human-readable paths — server-side
// DBKeyResolver resolves them at deploy time (see ControlRelations.Resolve in
// core).
type relationModel struct {
	Domain     types.String   `tfsdk:"domain"`
	Collection types.String   `tfsdk:"collection"`
	Path       types.String   `tfsdk:"path"`
	Note       types.String   `tfsdk:"note"`
	IsPrimary  types.Bool     `tfsdk:"is_primary"`
	Producer   *producerModel `tfsdk:"producer"`
}

type producerModel struct {
	Type types.String `tfsdk:"type"`
	Path types.String `tfsdk:"path"`
}

func relationsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "Subscription relations — which domain/collection this control reads occurrences from, and which plugin produces them.",
		Optional:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"domain": schema.StringAttribute{
					MarkdownDescription: "Domain identifier — UUID or path (e.g., `compliance.controls`). Resolved server-side.",
					Required:            true,
				},
				"collection": schema.StringAttribute{
					MarkdownDescription: "Collection identifier — UUID or path (e.g., `security`, `testing`). Resolved server-side.",
					Required:            true,
				},
				"path": schema.StringAttribute{
					MarkdownDescription: "Subscription I/O path (e.g., `checkmarx.sast`). NOT the collection identifier — this is the producer's path within the collection.",
					Optional:            true,
				},
				"note": schema.StringAttribute{
					MarkdownDescription: "Note context for this relation (e.g., `occurrence`).",
					Optional:            true,
				},
				"is_primary": schema.BoolAttribute{
					MarkdownDescription: "Whether this is the primary relation for the control.",
					Optional:            true,
				},
				"producer": schema.SingleNestedAttribute{
					MarkdownDescription: "The plugin or workflow that produces data for this relation.",
					Optional:            true,
					Attributes: map[string]schema.Attribute{
						"type": schema.StringAttribute{
							MarkdownDescription: "Producer type — `plugin`, `workflow`, etc.",
							Required:            true,
						},
						"path": schema.StringAttribute{
							MarkdownDescription: "Producer entity path (e.g., `checkmarx`).",
							Required:            true,
						},
					},
				},
			},
		},
	}
}

// toRelation maps one HCL row to the entity-side ControlRelationInput.
func (r relationModel) toRelation() fianu_entities.ControlRelationInput {
	rel := fianu_entities.ControlRelationInput{
		Domain:     r.Domain.ValueString(),
		Collection: r.Collection.ValueString(),
		IsPrimary:  r.IsPrimary.ValueBool(),
	}
	if !r.Path.IsNull() && !r.Path.IsUnknown() {
		s := r.Path.ValueString()
		rel.Path = &s
	}
	if !r.Note.IsNull() && !r.Note.IsUnknown() {
		s := r.Note.ValueString()
		rel.Note = &s
	}
	if r.Producer != nil {
		rel.Producer = &fianu_entities.ControlRelationProducer{
			Type: r.Producer.Type.ValueString(),
			Path: r.Producer.Path.ValueString(),
		}
	}
	return rel
}
