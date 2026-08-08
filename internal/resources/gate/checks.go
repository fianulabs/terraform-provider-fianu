// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package gate

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/fianulabs/terraform-provider-fianu/internal/resources/base"
)

// gateConfigModel is the gate-native check configuration — `detail.gate`,
// marshalled into `entities.ControlDetail.Gate` and versioned with the gate
// itself.
//
// This replaces the `gate_check_rule` entity pods the provider used to write
// through SetEntityPod. Core deleted that pod type; the rules now ride the
// same deploy call as the rest of the gate, so config changes are atomic with
// the entity instead of a second round-trip that could half-apply.
//
// `GateDetail.Pausing` is deliberately absent from this schema. A pause is
// operational state owned by `PATCH /gates/{key}/pause` and stored in its own
// `controls.gate_pausing` column, grafted onto reads — it is not versioned
// with the gate. Modelling it here would let an apply fight an active incident
// pause. Add it as a read-only computed attribute if operators ask to see it.
type gateConfigModel struct {
	// Enabled is the gate's master switch. Absent means off — an
	// unconfigured gate must never activate automation on its own.
	Enabled types.Bool `tfsdk:"enabled"`
	// Checks are independently-evaluated rules. Every matched check
	// contributes its protection level; most restrictive wins.
	Checks []gateCheckModel `tfsdk:"checks"`
}

// gateCheckModel is one rule in `detail.gate.checks`. Each of the several
// `gate_check_rule` pods a gate used to hold is now one entry here.
type gateCheckModel struct {
	Name             types.String          `tfsdk:"name"`
	Enabled          types.Bool            `tfsdk:"enabled"`
	ProtectionLevel  types.String          `tfsdk:"protection_level"`
	GatingSources    []types.String        `tfsdk:"gating_sources"`
	CompletionAction types.String          `tfsdk:"completion_action"`
	Matching         []protectedScopeModel `tfsdk:"matching"`
}

// protectedScopeModel is a scope within a check's matching list. Each scope
// binds a CEL expression group OR a set of index references to its own
// ProtectionLevel — letting one check say "enforce on production repos, check
// elsewhere" without splitting into two checks.
//
// Mirrors the three input shapes of fianu_entities.PolicyAssetGroup (which
// GateProtectedScope embeds): asset+expressions, indexes, asset only.
type protectedScopeModel struct {
	ProtectionLevel types.String                 `tfsdk:"protection_level"`
	Asset           *base.CriteriaAssetModel     `tfsdk:"asset"`
	Expressions     []base.ExpressionModel       `tfsdk:"expressions"`
	Indexes         []base.CriteriaIndexRefModel `tfsdk:"indexes"`
}

func gateConfigAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Gate-native check configuration (`detail.gate`) — the master switch and the rules that decide whether the gate runs, at what protection level, and whether it drives SCM automation. Versioned with the gate. Replaces the removed `gate_check_rule` entity pods.",
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "The gate's master switch. Omitted means **off** — an unconfigured gate never activates automation on its own. Set `true` to turn the gate on.",
				Optional:            true,
			},
			"checks": checksAttribute(),
		},
	}
}

func checksAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "Independently-evaluated rules. Every check whose `matching` scope applies contributes its protection level, and the most restrictive wins (`enforce` > `check` > inherit).",
		Optional:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{
					MarkdownDescription: "Operator-facing label. Not an identifier — checks are positional within the list.",
					Optional:            true,
				},
				"enabled": schema.BoolAttribute{
					MarkdownDescription: "Whether this check participates in resolution. Omitted means **enabled** — the opposite default from the gate-level `enabled`, because a check only exists because someone authored it.",
					Optional:            true,
				},
				"protection_level": schema.StringAttribute{
					MarkdownDescription: "Default protection level when no `matching` scope overrides it. `enforce` blocks deployments on gate failure; `check` runs the gate but always approves. Defaults to `enforce`.",
					Optional:            true,
					Validators: []validator.String{
						stringvalidator.OneOf(
							string(fianu_entities.ProtectionLevelEnforce),
							string(fianu_entities.ProtectionLevelCheck),
						),
					},
				},
				"gating_sources": schema.ListAttribute{
					MarkdownDescription: "Deciding systems that must **all** pass for this check to pass. Defaults to `[\"fianu\"]`. Additional sources are platform instance keys whose platform declares the `gatingSource` capability.",
					Optional:            true,
					ElementType:         types.StringType,
				},
				"completion_action": schema.StringAttribute{
					MarkdownDescription: "SCM automation to drive after evaluation completes. `post_check_status` posts a GitHub Check Run / GitLab Commit Status; `auto_approve_pr` approves the PR/MR when all enforced gates pass. Omit for no automation.",
					Optional:            true,
					Validators: []validator.String{
						stringvalidator.OneOf(
							string(fianu_entities.GateCompletionActionPostCheckStatus),
							string(fianu_entities.GateCompletionActionAutoApprovePR),
						),
					},
				},
				"matching": schema.ListNestedAttribute{
					MarkdownDescription: "CEL scope groups, each with an optional per-scope protection-level override. Empty matches unconditionally, so the check's top-level `protection_level` applies to all gated traffic.",
					Optional:            true,
					NestedObject: schema.NestedAttributeObject{
						// Per-element validator: enforces the three-shape rule on
						// each matching scope so plan surfaces bad shapes before
						// apply, matching the criteria validator on
						// variations[*].criteria.
						Validators: []validator.Object{
							base.CriteriaShapeValidator(),
						},
						Attributes: matchingScopeAttributes(),
					},
				},
			},
		},
	}
}

// matchingScopeAttributes is the shared criteria shape plus this scope's own
// protection-level override — the whole reason a check carries several scopes
// instead of one.
func matchingScopeAttributes() map[string]schema.Attribute {
	attrs := base.CriteriaShapeAttributes()
	attrs["protection_level"] = schema.StringAttribute{
		MarkdownDescription: "Protection level for this scope. `enforce` or `check`. Omit to inherit the check's top-level level.",
		Optional:            true,
		Validators: []validator.String{
			stringvalidator.OneOf(
				string(fianu_entities.ProtectionLevelEnforce),
				string(fianu_entities.ProtectionLevelCheck),
			),
		},
	}
	return attrs
}

// buildGateConfig converts the HCL `detail.gate` block into the wire-side
// GateDetail. Returns nil when the block is absent so the gate deploys with no
// gate-native config, which the server reads as "apply the surface's own
// default" (see GateDetail's doc comment in core).
//
// Pausing is left nil: it lives in its own column server-side and is never
// authored from Terraform.
func buildGateConfig(m *gateConfigModel) *fianu_entities.GateDetail {
	if m == nil {
		return nil
	}
	d := &fianu_entities.GateDetail{}
	if !m.Enabled.IsNull() && !m.Enabled.IsUnknown() {
		enabled := m.Enabled.ValueBool()
		d.Enabled = &enabled
	}
	for _, c := range m.Checks {
		d.Checks = append(d.Checks, buildGateCheck(c))
	}
	return d
}

func buildGateCheck(m gateCheckModel) fianu_entities.GateCheck {
	protLevel := m.ProtectionLevel.ValueString()
	if protLevel == "" {
		protLevel = string(fianu_entities.ProtectionLevelEnforce)
	}

	check := fianu_entities.GateCheck{
		Name:             m.Name.ValueString(),
		ProtectionLevel:  fianu_entities.ProtectionLevel(protLevel),
		CompletionAction: fianu_entities.GateCompletionAction(m.CompletionAction.ValueString()),
		Matching:         buildMatching(m.Matching),
	}
	if !m.Enabled.IsNull() && !m.Enabled.IsUnknown() {
		enabled := m.Enabled.ValueBool()
		check.Enabled = &enabled
	}
	for _, s := range m.GatingSources {
		if s.IsNull() || s.IsUnknown() || s.ValueString() == "" {
			continue
		}
		check.GatingSources = append(check.GatingSources, s.ValueString())
	}
	return check
}

// buildMatching translates HCL-side matching scopes into the wire shape.
// Each scope is a PolicyAssetGroup (built by base.AssetGroup, shared with
// policy criteria and notification rules) plus its own protection level.
func buildMatching(in []protectedScopeModel) []fianu_entities.GateProtectedScope {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.GateProtectedScope, len(in))
	for i, s := range in {
		out[i] = fianu_entities.GateProtectedScope{
			PolicyAssetGroup: *base.AssetGroup(s.Asset, s.Expressions, s.Indexes),
			ProtectionLevel:  fianu_entities.ProtectionLevel(s.ProtectionLevel.ValueString()),
		}
	}
	return out
}
