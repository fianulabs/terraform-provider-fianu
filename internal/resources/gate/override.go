// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package gate

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// overrideModel maps to entities.PolicyAssetOverride. Optional — when nil,
// the policy applies to whatever asset scope the target control declares.
type overrideModel struct {
	Asset overrideAssetModel `tfsdk:"asset"`
}

type overrideAssetModel struct {
	Types    []types.String `tfsdk:"types"`
	Explicit []types.String `tfsdk:"explicit"`
}

func overrideAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Asset scope override. When set, narrows or expands the asset set the policy applies to beyond the target control's declared scope.",
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

func (o *overrideModel) toEntity() *fianu_entities.PolicyAssetOverride {
	if o == nil {
		return nil
	}
	return &fianu_entities.PolicyAssetOverride{
		Asset: fianu_entities.PolicyAssetOverrideSection{
			Types:    stringSlice(o.Asset.Types),
			Explicit: stringSlice(o.Asset.Explicit),
		},
	}
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
