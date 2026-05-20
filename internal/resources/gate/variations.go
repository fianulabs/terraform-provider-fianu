// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package gate

import (
	"encoding/json"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// variationModel is one entry in detail.variations. Each variation pins the
// effect (apply or exempt) at a priority, with an arbitrary key→value map of
// metric overrides under `policy`. Optional `criteria` narrows the variation
// to assets matching a set of CEL expressions.
type variationModel struct {
	Effect   types.String   `tfsdk:"effect"`
	Priority types.Int64    `tfsdk:"priority"`
	Locked   types.Bool     `tfsdk:"locked"`
	Criteria *criteriaModel `tfsdk:"criteria"`
	// Policy is a JSON-encoded map[string]any of metric overrides. Kept as a
	// string because the shape is per-control (whatever the control's
	// policy_template declares as `measures`) and HCL can't express truly
	// dynamic maps without losing schema validation entirely.
	Policy types.String `tfsdk:"policy"`
}

func variationsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "Policy variations. Each variation pairs an effect (apply or exempt) with metric overrides for the target control. Server evaluates variations in priority order; the first matching variation wins.",
		Required:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"effect": schema.StringAttribute{
					MarkdownDescription: "What this variation does. `apply` runs the control with the supplied metric overrides; `exempt` skips evaluation entirely for matching assets. Defaults to `apply` when omitted (server-side default).",
					Optional:            true,
					Validators: []validator.String{
						stringvalidator.OneOf(
							string(fianu_entities.PolicyEffectApply),
							string(fianu_entities.PolicyEffectExempt),
						),
					},
				},
				"priority": schema.Int64Attribute{
					MarkdownDescription: "Evaluation priority. Lower numbers run first. Defaults to 0 when omitted.",
					Optional:            true,
				},
				"locked": schema.BoolAttribute{
					MarkdownDescription: "When true, prevents downstream tenants from overriding this variation. Defaults to false.",
					Optional:            true,
				},
				"policy": schema.StringAttribute{
					MarkdownDescription: "JSON-encoded map of metric overrides keyed by the control's policy_template measure names. Use `jsonencode({ required = true, vulnerabilities = { critical = { maximum = 0 } } })` to author.",
					Required:            true,
				},
				"criteria": criteriaAttribute(),
			},
		},
	}
}

// buildVariations translates HCL-side variations into the wire shape. An
// empty/nil input maps to an empty slice (not nil) so the server sees the
// `policy` JSON key with a stable shape.
func buildVariations(in []variationModel) []fianu_entities.PolicyVariation {
	out := make([]fianu_entities.PolicyVariation, len(in))
	for i, v := range in {
		out[i] = fianu_entities.PolicyVariation{
			PolicyEffect: fianu_entities.PolicyEffect(v.Effect.ValueString()),
			Priority:     int(v.Priority.ValueInt64()),
			Locked:       v.Locked.ValueBool(),
			Policy:       parsePolicyDetail(v.Policy.ValueString()),
			Criteria:     v.Criteria.toEntity(),
		}
	}
	return out
}

// parsePolicyDetail decodes the user-authored JSON blob into a
// map[string]any. Invalid JSON falls back to an empty map; the server will
// reject the deploy with a clearer error than swallowing it here would.
// Plan-time validation could enforce JSON syntax, but doing so requires
// custom validators — left as a follow-up.
func parsePolicyDetail(s string) fianu_entities.PolicyVariationDetail {
	if s == "" {
		return fianu_entities.PolicyVariationDetail{}
	}
	var out fianu_entities.PolicyVariationDetail
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return fianu_entities.PolicyVariationDetail{}
	}
	return out
}
