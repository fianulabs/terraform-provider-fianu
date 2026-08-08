// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package gate

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// overrideModel is the HCL asset-scope override on a gate's inline policy.
//
// It used to marshal straight into `entities.PolicyAssetOverride`. That type is
// deprecated — core's canonical write shape binds assets per criteria — so the
// provider now folds this block into `Detail.Assets` instead and lets the
// server derive the override itself. See toAssetRefs for why that resolves
// identically.
type overrideModel struct {
	Asset overrideAssetModel `tfsdk:"asset"`
}

type overrideAssetModel struct {
	Types    []types.String `tfsdk:"types"`
	Explicit []types.String `tfsdk:"explicit"`
}

func overrideAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Asset scope override. When set, narrows or expands the asset set the policy applies to beyond the target control's declared scope.\n\nPrefer per-variation `criteria.asset.type` for new configuration — it is the shape the server canonicalises to, and the one `fianu_policy` uses. This block is kept because a gate variation with no criteria has no other way to say what it applies to.",
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"asset": schema.SingleNestedAttribute{
				Required: true,
				Attributes: map[string]schema.Attribute{
					"types": schema.ListAttribute{
						MarkdownDescription: "Abstract asset types to target (e.g., `[\"repository\", \"module\"]`). Optional; defaults to whatever the target control declares.",
						Optional:            true,
						ElementType:         types.StringType,
					},
					"explicit": schema.ListAttribute{
						MarkdownDescription: "Explicit asset entity keys or UUIDs to target. Optional; coexists with `types` — assets matching either are in scope.",
						Optional:            true,
						ElementType:         types.StringType,
					},
				},
			},
		},
	}
}

// assetTypeExplicit routes an asset ref into the override's Explicit bucket
// server-side (`buildOverrideFromAssets`, pkg/policies/service.go).
//
// It is a bare string rather than a typed constant on purpose: the matching
// constant is `db_vars.AbstractTypeExplicit`, which is itself deprecated, and
// naming it here would trade one deprecated reference for another. The server
// arm that reads this value keys off the literal `"asset"` alongside the
// deprecated constant, and is documented to stay until explicit targeting
// finishes migrating to the indexes pipeline.
const assetTypeExplicit = "asset"

// toAssetRefs converts the override into the asset refs that go on
// `Detail.Assets`.
//
// Writing `Detail.Override` directly is deprecated. It is also unnecessary:
// when `Detail.Override` is nil and `Detail.Assets` is populated, the server's
// resolvePolicy calls buildOverrideFromAssets to derive exactly the same
// override before resolving scope. So the two shapes resolve identically, and
// this one does not touch a deprecated field.
//
// The mapping mirrors the arms of buildOverrideFromAssets:
//
//   - a ref carrying only Path has no valid UUID, so it lands in Types —
//     matching what `override.asset.types` produced.
//   - a ref tagged assetTypeExplicit lands in Explicit verbatim, which
//     preserves the old behaviour for entity keys that aren't UUIDs.
func (o *overrideModel) toAssetRefs() []fianu_entities.PolicyAssetRef {
	if o == nil {
		return nil
	}
	refs := make([]fianu_entities.PolicyAssetRef, 0, len(o.Asset.Types)+len(o.Asset.Explicit))
	for _, t := range stringSlice(o.Asset.Types) {
		refs = append(refs, fianu_entities.PolicyAssetRef{Path: t})
	}
	for _, e := range stringSlice(o.Asset.Explicit) {
		refs = append(refs, fianu_entities.PolicyAssetRef{UUID: e, AssetType: assetTypeExplicit})
	}
	return refs
}

func stringSlice(in []types.String) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v.IsNull() || v.IsUnknown() {
			continue
		}
		out = append(out, v.ValueString())
	}
	return out
}
