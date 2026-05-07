package control

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
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
					MarkdownDescription: "Asset type — `module`, `repository`, `artifact`, etc.",
					Required:            true,
				},
				"target_asset_type_uuid": schema.StringAttribute{
					MarkdownDescription: "Server-side UUID of the asset type. Optional; resolved from `type` when omitted.",
					Optional:            true,
				},
				"cardinality": schema.StringAttribute{
					MarkdownDescription: "`single` or `multiple`. Defaults to multiple when unset.",
					Optional:            true,
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

func buildAssets(in []controlAssetModel) []fianu_entities.ControlAsset {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.ControlAsset, len(in))
	for i, a := range in {
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
		out[i] = asset
	}
	return out
}
