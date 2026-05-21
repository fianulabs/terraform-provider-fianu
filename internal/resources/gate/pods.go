// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package gate

import (
	"encoding/json"

	"github.com/fianulabs/core/v2/external/db/pods"
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/fianulabs/core/v2/external/pkg/cel"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// podType is the fixed pod_type the provider sets on gate-check-rule pods.
// Matches `entities.GateCheckRuleValue`'s server-side pod row type.
const podType = "gate_check_rule"

// podModel is one pipeline automation rule attached to the gate. Each pod
// row is uniquely identified server-side by (entity_id, pod_type, key) —
// the provider scopes all of these to the gate's entity_id and uses
// `pod_type = "gate_check_rule"`. Key is user-authored and must be unique
// within the gate.
type podModel struct {
	Key              types.String          `tfsdk:"key"`
	Name             types.String          `tfsdk:"name"`
	Description      types.String          `tfsdk:"description"`
	Enabled          types.Bool            `tfsdk:"enabled"`
	ProtectionLevel  types.String          `tfsdk:"protection_level"`
	CompletionAction types.String          `tfsdk:"completion_action"`
	Matching         []protectedScopeModel `tfsdk:"matching"`
}

// protectedScopeModel is a scope within a pod's matching list. Each scope
// binds a CEL expression group to its own ProtectionLevel — letting one pod
// say "enforce on production repos, check elsewhere" without splitting into
// two pods.
type protectedScopeModel struct {
	ProtectionLevel types.String      `tfsdk:"protection_level"`
	Expressions     []expressionModel `tfsdk:"expressions"`
}

func podsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "Pipeline automation rules attached to this gate. Each pod is a JSON-valued row scoped to the gate (`pod_type = \"gate_check_rule\"`). The pod's `protection_level` sets the default enforcement, and `matching[]` lets per-scope CEL expressions override the protection level for subsets of the gate's traffic.",
		Optional:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"key": schema.StringAttribute{
					MarkdownDescription: "Pod key — unique per gate. Stable identifier; the provider uses this to compute add/update/delete diffs across applies.",
					Required:            true,
				},
				"name": schema.StringAttribute{
					MarkdownDescription: "Human-readable name for the pod.",
					Optional:            true,
				},
				"description": schema.StringAttribute{
					MarkdownDescription: "Free-form description.",
					Optional:            true,
				},
				"enabled": schema.BoolAttribute{
					MarkdownDescription: "Whether this pod participates in gating. Defaults to true.",
					Optional:            true,
				},
				"protection_level": schema.StringAttribute{
					MarkdownDescription: "Default protection level when no `matching` scope applies. `enforce` blocks deployments on gate failure; `check` runs the gate but always approves. Defaults to `enforce`.",
					Optional:            true,
					Validators: []validator.String{
						stringvalidator.OneOf(
							string(fianu_entities.ProtectionLevelEnforce),
							string(fianu_entities.ProtectionLevelCheck),
						),
					},
				},
				"completion_action": schema.StringAttribute{
					MarkdownDescription: "Optional post-evaluation action identifier (server-specific).",
					Optional:            true,
				},
				"matching": schema.ListNestedAttribute{
					MarkdownDescription: "Scoped overrides: each entry binds a CEL expression group to its own protection level. Most-restrictive wins (`enforce` > `check` > inherit). When omitted the pod's top-level `protection_level` applies to all gated traffic.",
					Optional:            true,
					NestedObject: schema.NestedAttributeObject{
						Attributes: map[string]schema.Attribute{
							"protection_level": schema.StringAttribute{
								MarkdownDescription: "Protection level for this scope. `enforce` or `check`. Omit to inherit the pod's top-level level.",
								Optional:            true,
								Validators: []validator.String{
									stringvalidator.OneOf(
										string(fianu_entities.ProtectionLevelEnforce),
										string(fianu_entities.ProtectionLevelCheck),
									),
								},
							},
							"expressions": schema.ListNestedAttribute{
								MarkdownDescription: "CEL expressions defining the scope. Combine clauses with `&&`/`||` inside a single expression; multiple entries are AND'd together.",
								Required:            true,
								NestedObject: schema.NestedAttributeObject{
									Attributes: map[string]schema.Attribute{
										"expression": schema.StringAttribute{
											MarkdownDescription: "CEL expression evaluated against the gated event (e.g., `asset.scm.repository startsWith 'prod-'`).",
											Required:            true,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// buildPod converts the HCL pod model into the wire-side pods.Pod. The
// value field is a JSON-marshalled GateCheckRuleValue.
func buildPod(m podModel) (pods.Pod, error) {
	protLevel := m.ProtectionLevel.ValueString()
	if protLevel == "" {
		protLevel = string(fianu_entities.ProtectionLevelEnforce)
	}

	enabled := true
	if !m.Enabled.IsNull() && !m.Enabled.IsUnknown() {
		enabled = m.Enabled.ValueBool()
	}

	value := fianu_entities.GateCheckRuleValue{
		ProtectionLevel:  protLevel,
		CompletionAction: m.CompletionAction.ValueString(),
		Enabled:          &enabled,
		Matching:         buildMatching(m.Matching),
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return pods.Pod{}, err
	}

	pod := pods.Pod{
		PodType: podType,
		Key:     m.Key.ValueString(),
		Value:   raw,
	}
	if v := m.Name; !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		s := v.ValueString()
		pod.Name = &s
	}
	if v := m.Description; !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		s := v.ValueString()
		pod.Description = &s
	}
	pod.Enabled = &enabled
	return pod, nil
}

// buildMatching translates HCL-side matching scopes into the wire shape.
func buildMatching(in []protectedScopeModel) []fianu_entities.GateProtectedScope {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.GateProtectedScope, len(in))
	for i, s := range in {
		scope := fianu_entities.GateProtectedScope{
			ProtectionLevel: fianu_entities.ProtectionLevel(s.ProtectionLevel.ValueString()),
		}
		// PolicyAssetGroup embedded; default combineWith=AND.
		scope.PolicyAssetGroup.CombineWith = "AND"
		if len(s.Expressions) > 0 {
			scope.PolicyAssetGroup.Expressions = make([]fianu_entities.PolicyAssetGroupExpression, len(s.Expressions))
			for j, e := range s.Expressions {
				raw := e.Expression.ValueString()
				// Pre-parse via cel.ParseExpression so ExprSource carries
				// the canonical form the validator expects. See
				// internal/resources/policy/criteria.go for full rationale.
				parsed, err := cel.ParseExpression(raw)
				if err != nil {
					parsedPtr := raw
					scope.PolicyAssetGroup.Expressions[j] = fianu_entities.PolicyAssetGroupExpression{Seq: j + 1, Expr: &parsedPtr}
					continue
				}
				scope.PolicyAssetGroup.Expressions[j] = fianu_entities.PolicyAssetGroupExpression{
					Seq:         j + 1,
					ExprSource:  parsed,
					ExprDisplay: raw,
				}
			}
		}
		out[i] = scope
	}
	return out
}
