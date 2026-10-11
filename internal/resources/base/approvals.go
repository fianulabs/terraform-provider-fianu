// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package base

import (
	"fmt"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// `entities.Approvals` is the ordered approval-rule list a governing entity
// (control, gate, collection) carries in its spec. Defined once here so each
// resource composes it. The server validates the rules (CEL criteria, approver
// specs) on deploy; the schema only catches enum typos at plan time.
//
// Unset means "not managed here": the server keeps the entity's current rules,
// so rules edited in the Console survive an apply. `rules = []` clears them.

// ApprovalsModel is the `approvals` block.
type ApprovalsModel struct {
	Rules []ApprovalRuleModel `tfsdk:"rules"`
}

// ApprovalRuleModel is one rule.
type ApprovalRuleModel struct {
	ID           types.String           `tfsdk:"id"`
	Name         types.String           `tfsdk:"name"`
	Description  types.String           `tfsdk:"description"`
	Enabled      types.Bool             `tfsdk:"enabled"`
	Enforced     types.Bool             `tfsdk:"enforced"`
	SubjectTypes []types.String         `tfsdk:"subject_types"`
	History      *ApprovalHistoryModel  `tfsdk:"history"`
	Criteria     *ApprovalCriteriaModel `tfsdk:"criteria"`
	Action       ApprovalActionModel    `tfsdk:"action"`
}

// ApprovalHistoryModel identifies repeat requests for $history.occurrence.
type ApprovalHistoryModel struct {
	Measure   types.String `tfsdk:"measure"`
	Key       types.String `tfsdk:"key"`
	Count     types.String `tfsdk:"count"`
	Scope     types.String `tfsdk:"scope"`
	MaxAssets types.Int64  `tfsdk:"max_assets"`
}

// ApprovalCriteriaModel is the rule's condition.
type ApprovalCriteriaModel struct {
	CombineWith types.String   `tfsdk:"combine_with"`
	Expressions []types.String `tfsdk:"expressions"`
}

// ApprovalActionModel is what a matching rule does.
type ApprovalActionModel struct {
	Type         types.String               `tfsdk:"type"`
	Distinct     types.Bool                 `tfsdk:"distinct"`
	Requirements []ApprovalRequirementModel `tfsdk:"requirements"`
}

// ApprovalRequirementModel is one approval condition of a require rule.
type ApprovalRequirementModel struct {
	Label     types.String   `tfsdk:"label"`
	Approvers []types.String `tfsdk:"approvers"`
	Count     types.Int64    `tfsdk:"count"`
}

var approvalSubjectTypes = []string{
	string(fianu_entities.ApprovalSubjectException), string(fianu_entities.ApprovalSubjectPolicy),
	string(fianu_entities.ApprovalSubjectControl), string(fianu_entities.ApprovalSubjectGate),
	string(fianu_entities.ApprovalSubjectDomain), string(fianu_entities.ApprovalSubjectCollection),
	string(fianu_entities.ApprovalSubjectRelease), string(fianu_entities.ApprovalSubjectAssetUpdate),
}

// ApprovalsAttribute is the shared `approvals` attribute.
func ApprovalsAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Approval rules: who must approve requests this entity governs, or when they approve automatically. " +
			"Rules are evaluated in order and the first match wins. Leave unset to keep the rules managed in the Console; " +
			"set `rules = []` to clear them.",
		Optional: true,
		Attributes: map[string]schema.Attribute{
			"rules": schema.ListNestedAttribute{
				MarkdownDescription: "Ordered rules; the first match wins.",
				Required:            true,
				NestedObject:        schema.NestedAttributeObject{Attributes: approvalRuleAttributes()},
			},
		},
	}
}

func approvalRuleAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":          schema.StringAttribute{MarkdownDescription: "Stable slug, unique within the block.", Required: true},
		"name":        schema.StringAttribute{MarkdownDescription: "Display name.", Optional: true},
		"description": schema.StringAttribute{MarkdownDescription: "Description.", Optional: true},
		"enabled": schema.BoolAttribute{
			MarkdownDescription: "A disabled rule is kept but skipped. Defaults to `true`.",
			Optional:            true, Computed: true, Default: booldefault.StaticBool(true),
		},
		"enforced": schema.BoolAttribute{
			MarkdownDescription: "Evaluated before every ordinary rule, farthest governor first, so a domain or collection floor beats a control's auto-approve.",
			Optional:            true,
		},
		"subject_types": schema.ListAttribute{
			MarkdownDescription: "Request kinds the rule applies to; unset means every kind.",
			Optional:            true, ElementType: types.StringType,
			Validators: []validator.List{listvalidator.ValueStringsAre(stringvalidator.OneOf(approvalSubjectTypes...))},
		},
		"history": schema.SingleNestedAttribute{
			MarkdownDescription: "What counts as the same finding for `$history.occurrence`.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"measure": schema.StringAttribute{
					MarkdownDescription: "List measure holding the line items, e.g. `vulnerabilities.alerts`. " +
						"Required with scope `control`; with scope `asset`, unset counts every request covering the asset.",
					Optional: true,
				},
				"key": schema.StringAttribute{
					MarkdownDescription: "Line-item field identifying a finding, e.g. `alert_id`. Unset when the measure is a list of strings (e.g. CVE ids).",
					Optional:            true,
				},
				"count": schema.StringAttribute{
					MarkdownDescription: "`submitted` (default) counts every earlier request; `approved` only approved ones.",
					Optional:            true,
					Validators: []validator.String{stringvalidator.OneOf(
						string(fianu_entities.ApprovalHistoryCountSubmitted), string(fianu_entities.ApprovalHistoryCountApproved))},
				},
				"scope": schema.StringAttribute{
					MarkdownDescription: "`control` (default) counts a finding across every asset; `asset` counts per asset the request covers, " +
						"so the first exception on each asset is occurrence 1. Scope `asset` needs `subject_types` of `exception` and/or `policy`.",
					Optional: true,
					Validators: []validator.String{stringvalidator.OneOf(
						string(fianu_entities.ApprovalHistoryScopeControl), string(fianu_entities.ApprovalHistoryScopeAsset))},
				},
				"max_assets": schema.Int64Attribute{
					MarkdownDescription: fmt.Sprintf("With scope `asset`, the most assets a request may cover and still count per asset "+
						"(default %d, at most %d). A wider request does not match a per-asset auto-approve, so a later rule (e.g. the owner) applies, "+
						"and it never counts toward its assets' history.",
						fianu_entities.DefaultApprovalHistoryMaxAssets, fianu_entities.MaxApprovalHistoryMaxAssets),
					Optional:   true,
					Validators: []validator.Int64{int64validator.Between(1, fianu_entities.MaxApprovalHistoryMaxAssets)},
				},
			},
		},
		"criteria": schema.SingleNestedAttribute{
			MarkdownDescription: "CEL over `$request`, `$history`, `$series`, `$evidence`, `$subject`, `$target`, `$requester`; unset means always.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"combine_with": schema.StringAttribute{
					MarkdownDescription: "`AND` (default) or `OR`.", Optional: true,
					Validators: []validator.String{stringvalidator.OneOf("AND", "OR")},
				},
				"expressions": schema.ListAttribute{
					MarkdownDescription: "CEL expressions, e.g. `$history.occurrence >= 3`.",
					Required:            true, ElementType: types.StringType,
				},
			},
		},
		"action": schema.SingleNestedAttribute{
			MarkdownDescription: "What a matching rule does.",
			Required:            true,
			Attributes: map[string]schema.Attribute{
				"type": schema.StringAttribute{
					MarkdownDescription: "`require` or `auto_approve`.", Required: true,
					Validators: []validator.String{stringvalidator.OneOf(
						string(fianu_entities.ApprovalActionRequire), string(fianu_entities.ApprovalActionAutoApprove))},
				},
				"distinct": schema.BoolAttribute{MarkdownDescription: "Each requirement must be met by a different person.", Optional: true},
				"requirements": schema.ListNestedAttribute{
					MarkdownDescription: "One approval condition each (`require` only).",
					Optional:            true,
					NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
						"label": schema.StringAttribute{MarkdownDescription: "Condition label.", Required: true},
						"approvers": schema.ListAttribute{
							MarkdownDescription: "`user:<sub>`, `role:<relation>`, `role:<relation>@<anchor>` or `role:<relation>@each:<anchor>`; " +
								"an anchor is `control`, `collection`, `domain`, `subject` or an asset type path.",
							Required: true, ElementType: types.StringType,
						},
						"count": schema.Int64Attribute{
							MarkdownDescription: "Approvals needed.", Required: true,
							Validators: []validator.Int64{int64validator.AtLeast(1)},
						},
					}},
				},
			},
		},
	}
}

// BuildApprovals converts the HCL block into the wire shape. Nil stays nil so
// the server keeps the current rules; an empty list is sent as `[]` to clear.
func BuildApprovals(in *ApprovalsModel) *fianu_entities.Approvals {
	if in == nil {
		return nil
	}
	out := &fianu_entities.Approvals{Rules: make([]fianu_entities.ApprovalRule, 0, len(in.Rules))}
	for _, r := range in.Rules {
		out.Rules = append(out.Rules, r.toRule())
	}
	return out
}

func (r ApprovalRuleModel) toRule() fianu_entities.ApprovalRule {
	rule := fianu_entities.ApprovalRule{
		ID:          r.ID.ValueString(),
		Name:        r.Name.ValueString(),
		Description: r.Description.ValueString(),
		Enabled:     r.Enabled.ValueBool(),
		Enforced:    r.Enforced.ValueBool(),
		Action: fianu_entities.ApprovalAction{
			Type:     fianu_entities.ApprovalActionType(r.Action.Type.ValueString()),
			Distinct: r.Action.Distinct.ValueBool(),
		},
	}
	for _, s := range r.SubjectTypes {
		rule.SubjectTypes = append(rule.SubjectTypes, fianu_entities.ApprovalSubjectType(s.ValueString()))
	}
	if h := r.History; h != nil {
		rule.History = &fianu_entities.ApprovalHistory{Measure: h.Measure.ValueString(), Key: h.Key.ValueString(),
			Count:     fianu_entities.ApprovalHistoryCount(h.Count.ValueString()),
			Scope:     fianu_entities.ApprovalHistoryScope(h.Scope.ValueString()),
			MaxAssets: int(h.MaxAssets.ValueInt64())}
	}
	if c := r.Criteria; c != nil {
		rule.Criteria = &fianu_entities.ApprovalCriteria{CombineWith: c.CombineWith.ValueString()}
		for _, e := range c.Expressions {
			rule.Criteria.Expressions = append(rule.Criteria.Expressions, fianu_entities.ApprovalExpression{ExprSource: e.ValueString()})
		}
	}
	for _, q := range r.Action.Requirements {
		req := fianu_entities.ApprovalRequirement{Label: q.Label.ValueString(), Count: int(q.Count.ValueInt64())}
		for _, a := range q.Approvers {
			req.Approvers = append(req.Approvers, a.ValueString())
		}
		rule.Action.Requirements = append(rule.Action.Requirements, req)
	}
	return rule
}
