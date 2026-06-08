// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package index

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/fianulabs/core/v2/external/pkg/cel"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// indexDetailModel mirrors fianu_entities.IndexDetail minus server-resolved
// fields (the AssetType UUID — server derives it from AssetTypePath at
// deploy time).
type indexDetailModel struct {
	Description         types.String           `tfsdk:"description"`
	AssetType           types.String           `tfsdk:"asset_type"`
	DependentAssetTypes []types.String         `tfsdk:"dependent_asset_types"`
	CombineWith         types.String           `tfsdk:"combine_with"`
	Kind                types.String           `tfsdk:"kind"`
	Expressions         []indexExpressionModel `tfsdk:"expressions"`
}

type indexExpressionModel struct {
	Source types.String `tfsdk:"source"`
}

func detailAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Index payload — the asset-type binding and CEL expressions that define membership.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"description": schema.StringAttribute{
				MarkdownDescription: "Optional human-readable description.",
				Optional:            true,
			},
			"asset_type": schema.StringAttribute{
				MarkdownDescription: "Abstract asset type this index materialises members for. Built-in values include `application`, `repository`, `module`, `artifact`, `ticket`, `jira_issue`, `jira_version`, `jira_project`, `release`; orgs can register additional abstract asset types via the Console. The server resolves this path to the corresponding `abstract_asset` UUID at deploy time. Changing this forces replacement — switching asset_type invalidates every existing member and downstream policy/gate edges, so the destroy+create surfaces the cost explicitly in the plan.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"dependent_asset_types": schema.ListAttribute{
				MarkdownDescription: "Other asset types whose changes invalidate this index. Used by the recompute scheduler to detect when an out-of-band update on a referenced asset type should trigger a refresh. Same value space as `asset_type` (built-ins + org-registered abstract asset types).",
				Optional:            true,
				ElementType:         types.StringType,
			},
			"combine_with": schema.StringAttribute{
				MarkdownDescription: "How expressions combine when computing members. `AND` intersects member sets across expressions; `OR` unions them. Server defaults to `AND` when omitted.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.OneOf(
						string(fianu_entities.IndexCombineAnd),
						string(fianu_entities.IndexCombineOr),
					),
				},
			},
			"kind": schema.StringAttribute{
				MarkdownDescription: "Recompute strategy. `write` (server default) re-evaluates membership when referenced assets change; `write-ahead` precomputes membership for write-heavy workloads. The `default` kind is reserved for the per-asset-type catch-all index and shouldn't be authored by hand.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.OneOf(
						string(fianu_entities.IndexKindWrite),
						string(fianu_entities.IndexKindWriteAhead),
					),
				},
			},
			"expressions": schema.ListNestedAttribute{
				MarkdownDescription: "Ordered CEL expressions evaluated against assets at recompute time. Combine with `combine_with` (AND/OR). At least one expression is required for `write` / `write-ahead` kinds.",
				Required:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"source": schema.StringAttribute{
							MarkdownDescription: "CEL expression authored against the asset (e.g., `asset.scm.repository startsWith 'prod-'`). The provider pre-parses to the canonical form the server's validator expects; the raw text is preserved in the displayed CEL.",
							Required:            true,
						},
					},
				},
			},
		},
	}
}

// applyDetail writes the HCL detail model onto a *fianu_entities.Index built
// by entities.NewIndex (which already applies safe defaults — AND, write,
// empty slices). Caller is responsible for path/name.
func applyDetail(e *fianu_entities.Index, d indexDetailModel) {
	if v := d.Description; !v.IsNull() && !v.IsUnknown() {
		e.Detail.Description = v.ValueString()
	}
	if v := d.AssetType; !v.IsNull() && !v.IsUnknown() {
		e.Detail.AssetTypePath = v.ValueString()
	}
	if v := d.CombineWith; !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		e.Detail.CombineWith = fianu_entities.IndexCombineOperator(v.ValueString())
	}
	if v := d.Kind; !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		e.Detail.Kind = fianu_entities.IndexKind(v.ValueString())
	}
	if len(d.DependentAssetTypes) > 0 {
		out := make([]string, 0, len(d.DependentAssetTypes))
		for _, s := range d.DependentAssetTypes {
			if !s.IsNull() && !s.IsUnknown() {
				out = append(out, s.ValueString())
			}
		}
		e.Detail.DependentAssetTypes = out
	}
	if len(d.Expressions) > 0 {
		out := make([]fianu_entities.IndexExpressionSource, len(d.Expressions))
		for i, expr := range d.Expressions {
			raw := expr.Source.ValueString()
			parsed, err := cel.ParseExpression(raw)
			if err != nil {
				// IndexExpressionSource has no fallback `Expr *string` field
				// like PolicyAssetGroupExpression — Source is the only slot.
				// Pass the raw CEL through; the server will surface a
				// CompileExpression error if it's truly invalid.
				out[i] = fianu_entities.IndexExpressionSource{Seq: i + 1, Source: raw}
				continue
			}
			out[i] = fianu_entities.IndexExpressionSource{
				Seq:         i + 1,
				Source:      parsed,
				DisplayText: raw,
			}
		}
		e.Detail.Expressions = out
	}
}
