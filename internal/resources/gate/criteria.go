// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package gate

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/fianulabs/core/v2/external/pkg/cel"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// criteriaModel mirrors fianu_entities.PolicyAssetGroup minus the
// server-computed fields (UUID, timestamps, parsed AST, compiled SQL —
// those are populated by the server's CEL compiler when the policy is
// deployed). Only the user-authored bits land in HCL.
type criteriaModel struct {
	Name        types.String      `tfsdk:"name"`
	Description types.String      `tfsdk:"description"`
	CombineWith types.String      `tfsdk:"combine_with"`
	Expressions []expressionModel `tfsdk:"expressions"`
}

type expressionModel struct {
	Expression types.String `tfsdk:"expression"`
}

func criteriaAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Asset group criteria. Restricts this variation to assets matching a set of CEL expressions. When omitted, the variation applies to every asset in the policy's scope.",
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "Optional human-readable name for the asset group.",
				Optional:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Optional description of the criteria.",
				Optional:            true,
			},
			"combine_with": schema.StringAttribute{
				MarkdownDescription: "How the expressions combine. `AND` (all must match) or `OR` (any may match). Defaults to `AND`.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.OneOf("AND", "OR"),
				},
			},
			"expressions": schema.ListNestedAttribute{
				MarkdownDescription: "CEL expressions evaluated per-asset. Uses Fianu's CEL dialect — combine clauses inside a single expression with `&&`/`||`; multiple list entries are only needed when mixing OR semantics across separate predicates via `combine_with`.",
				Required:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"expression": schema.StringAttribute{
							MarkdownDescription: "CEL expression evaluated against the asset (e.g., `asset.name startsWith 'prod-'`).",
							Required:            true,
						},
					},
				},
			},
		},
	}
}

// toEntity converts the HCL criteria into the wire-side PolicyAssetGroup.
// Server-computed fields (UUID, timestamps, AST, SQL) stay zero — the
// server fills them in during deploy.
func (c *criteriaModel) toEntity() *fianu_entities.PolicyAssetGroup {
	if c == nil {
		return nil
	}
	g := &fianu_entities.PolicyAssetGroup{
		Name:        c.Name.ValueString(),
		Description: c.Description.ValueString(),
		CombineWith: c.CombineWith.ValueString(),
	}
	if g.CombineWith == "" {
		g.CombineWith = "AND"
	}
	if len(c.Expressions) > 0 {
		g.Expressions = make([]fianu_entities.PolicyAssetGroupExpression, len(c.Expressions))
		for i, e := range c.Expressions {
			raw := e.Expression.ValueString()
			// Pre-parse the user's pretty CEL into the canonical form the
			// server's validator expects. See internal/resources/policy/criteria.go
			// for the full rationale and source-of-truth reference.
			parsed, err := cel.ParseExpression(raw)
			if err != nil {
				parsedPtr := raw
				g.Expressions[i] = fianu_entities.PolicyAssetGroupExpression{Seq: i + 1, Expr: &parsedPtr}
				continue
			}
			g.Expressions[i] = fianu_entities.PolicyAssetGroupExpression{
				Seq:         i + 1,
				ExprSource:  parsed,
				ExprDisplay: raw,
			}
		}
	}
	return g
}
