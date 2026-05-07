package control

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// controlAssetModel is one entry in detail.assets — defines an asset type +
// its evaluation series (commit, tag, digest, uri, etc.).
type controlAssetModel struct {
	Type                types.String         `tfsdk:"type"`
	TargetAssetTypeUUID types.String         `tfsdk:"target_asset_type_uuid"`
	Cardinality         types.String         `tfsdk:"cardinality"`
	Series              []controlSeriesModel `tfsdk:"series"`
}

type controlSeriesModel struct {
	Name types.String `tfsdk:"name"`
	Code types.Int64  `tfsdk:"code"`
}

func assetsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "Asset types this control evaluates against. Each asset type lists the version `series` (commit, tag, digest, uri, …) the control fires on.",
		Optional:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"type": schema.StringAttribute{
					MarkdownDescription: "Asset type — `module`, `repository`, `artifact`, `application`, etc.",
					Required:            true,
					Validators: []validator.String{
						stringvalidator.OneOf(db_vars.AllAssetTypes()...),
					},
				},
				"target_asset_type_uuid": schema.StringAttribute{
					MarkdownDescription: "Server-side UUID of the asset type. Optional; resolved from `type` when omitted.",
					Optional:            true,
				},
				"cardinality": schema.StringAttribute{
					MarkdownDescription: "Applicability scope. One of `only`, `any`, `a`, `all`. Defaults to `all` server-side.",
					Optional:            true,
					Validators: []validator.String{
						stringvalidator.OneOf(db_vars.AllCardinalities()...),
					},
				},
				"series": schema.ListNestedAttribute{
					MarkdownDescription: "Version series — when this asset type produces evaluable events.",
					Optional:            true,
					NestedObject: schema.NestedAttributeObject{
						Attributes: map[string]schema.Attribute{
							"name": schema.StringAttribute{
								MarkdownDescription: "Series name (`commit`, `tag`, `digest`, `uri`).",
								Required:            true,
							},
							"code": schema.Int64Attribute{
								MarkdownDescription: "Numeric series code. Optional; server assigns from `name` when omitted.",
								Optional:            true,
							},
						},
					},
				},
			},
		},
	}
}

// toAsset maps one HCL row to the entity-side ControlAsset. The shape is
// thin enough that a one-pass copy is clearer than a builder.
func (a controlAssetModel) toAsset() fianu_entities.ControlAsset {
	asset := fianu_entities.ControlAsset{
		Type:        db_vars.AssetType(a.Type.ValueString()),
		Cardinality: db_vars.Cardinality(a.Cardinality.ValueString()),
	}
	if !a.TargetAssetTypeUUID.IsNull() && !a.TargetAssetTypeUUID.IsUnknown() {
		s := a.TargetAssetTypeUUID.ValueString()
		asset.TargetAssetTypeUUID = &s
	}
	if len(a.Series) > 0 {
		asset.Series = make([]fianu_entities.ControlAssetSeries, len(a.Series))
		for j, s := range a.Series {
			asset.Series[j] = fianu_entities.ControlAssetSeries{
				Name: db_vars.SeriesName(s.Name.ValueString()),
				Code: db_vars.SeriesCode(s.Code.ValueInt64()),
			}
		}
	}
	return asset
}
