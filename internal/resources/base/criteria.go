// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package base

import (
	"fmt"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	"github.com/fianulabs/core/v2/external/pkg/cel"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// `fianu_entities.PolicyAssetGroup` is the platform's one asset-matching
// primitive. It shows up in three unrelated-looking places:
//
//   - `fianu_policy`   — `detail.variations[].criteria`
//   - `fianu_gate`     — `detail.gate.checks[].matching[]` (via GateProtectedScope,
//     which embeds PolicyAssetGroup)
//   - `fianu_notification` — `rules`, which decides whether a notification fires
//
// One wire shape, one CEL evaluator, one authoring surface. This file is the
// single HCL-side definition; each resource composes it rather than restating
// it. Before this existed, gate and policy carried byte-identical copies that
// had already started drifting in their doc strings.
//
// The three input shapes are mutually exclusive — server-side `IsValid`
// enforces, and CriteriaShapeValidator surfaces the same rule at plan time:
//
//   - asset + expressions   → server spawns a private content-addressed index
//   - indexes (no asset)    → references existing fianu_index entities by id or path
//   - asset only (no exprs) → unscoped; server links the default index for that
//     asset type

// CriteriaAssetModel is the per-criteria asset binding.
type CriteriaAssetModel struct {
	Type types.String `tfsdk:"type"`
}

// CriteriaIndexRefModel references an existing index by id or path.
type CriteriaIndexRefModel struct {
	ID   types.String `tfsdk:"id"`
	Path types.String `tfsdk:"path"`
}

// ExpressionModel is one CEL expression in a criteria's expression list.
type ExpressionModel struct {
	Expression types.String `tfsdk:"expression"`
}

// CriteriaModel mirrors fianu_entities.PolicyAssetGroup minus the
// server-computed fields (UUID, timestamps, parsed AST, compiled SQL — those
// are populated by the server's CEL compiler on deploy). Only the
// user-authored bits land in HCL.
type CriteriaModel struct {
	Name        types.String            `tfsdk:"name"`
	Description types.String            `tfsdk:"description"`
	CombineWith types.String            `tfsdk:"combine_with"`
	Asset       *CriteriaAssetModel     `tfsdk:"asset"`
	Expressions []ExpressionModel       `tfsdk:"expressions"`
	Indexes     []CriteriaIndexRefModel `tfsdk:"indexes"`
}

// CriteriaShapeAttributes returns the three mutually-exclusive shape
// attributes — `asset`, `expressions`, `indexes`. Callers that need the full
// criteria block use CriteriaAttribute; callers that wrap these in their own
// object (a gate check's `matching[]` entry adds `protection_level`) compose
// this map instead.
//
// The names here are load-bearing: CriteriaShapeValidator looks them up by
// path, so a caller that renames one silently loses plan-time validation.
func CriteriaShapeAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"asset": schema.SingleNestedAttribute{
			MarkdownDescription: "Asset binding. Required when `expressions` are supplied OR when the scope is unscoped (no expressions and no indexes). Omit when `indexes` is set — the linked index already carries the asset type.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"type": schema.StringAttribute{
					MarkdownDescription: "Abstract asset type (e.g., `repository`, `application`, `module`). Built-ins are listed in the Console; orgs can register additional abstract asset types.",
					Required:            true,
				},
			},
		},
		"expressions": schema.ListNestedAttribute{
			MarkdownDescription: "CEL expressions evaluated per-asset. Uses Fianu's CEL dialect — combine clauses inside a single expression with `&&`/`||`; multiple list entries are only needed when mixing OR semantics across separate predicates. Mutually exclusive with `indexes`.",
			Optional:            true,
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"expression": schema.StringAttribute{
						MarkdownDescription: "CEL expression evaluated against the asset (e.g., `asset.name startsWith 'prod-'`).",
						Required:            true,
					},
				},
			},
		},
		"indexes": schema.ListNestedAttribute{
			MarkdownDescription: "References to existing indexes (by id or path). Mutually exclusive with `expressions` and `asset` — the linked index already carries asset type and CEL.",
			Optional:            true,
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"id": schema.StringAttribute{
						MarkdownDescription: "UUID of an existing index. Mutually exclusive with `path` within a single entry.",
						Optional:            true,
					},
					"path": schema.StringAttribute{
						MarkdownDescription: "Entity path of an existing index (e.g., from `fianu_index.foo.path`). Mutually exclusive with `id` within a single entry.",
						Optional:            true,
					},
				},
			},
		},
	}
}

// CriteriaAttribute returns the full criteria block — the three shape
// attributes plus name/description/combine_with — wired to the shape
// validator.
//
// scopeNoun names what the criteria narrows, and is interpolated into the
// description ("every asset in the <scopeNoun>'s scope"). Pass "policy",
// "gate", etc.
func CriteriaAttribute(scopeNoun string) schema.SingleNestedAttribute {
	attrs := CriteriaShapeAttributes()
	attrs["name"] = schema.StringAttribute{
		MarkdownDescription: "Optional human-readable name for the asset group.",
		Optional:            true,
	}
	attrs["description"] = schema.StringAttribute{
		MarkdownDescription: "Optional description of the criteria.",
		Optional:            true,
	}
	attrs["combine_with"] = schema.StringAttribute{
		MarkdownDescription: "How the expressions combine. `AND` (all must match) or `OR` (any may match). Defaults to `AND`.",
		Optional:            true,
		Validators: []validator.String{
			stringvalidator.OneOf("AND", "OR"),
		},
	}
	return schema.SingleNestedAttribute{
		MarkdownDescription: fmt.Sprintf("Asset group criteria. Restricts this to assets matching either a set of CEL expressions or one or more existing indexes. When omitted, it applies to every asset in the %s's scope.", scopeNoun),
		Optional:            true,
		Validators: []validator.Object{
			CriteriaShapeValidator(),
		},
		Attributes: attrs,
	}
}

// AssetGroup builds the wire-side PolicyAssetGroup from the three shape
// fields. Server-computed fields (UUID, timestamps, AST, SQL) stay zero — the
// server fills them in during deploy.
//
// Callers holding a full CriteriaModel should use ToEntity, which also carries
// name/description/combine_with.
func AssetGroup(asset *CriteriaAssetModel, exprs []ExpressionModel, indexes []CriteriaIndexRefModel) *fianu_entities.PolicyAssetGroup {
	g := &fianu_entities.PolicyAssetGroup{CombineWith: "AND"}

	// Provider boundary: convert types.String → variables.AssetType once here.
	// Internal Go callers downstream see the typed value with no further casts.
	if asset != nil && asset.Type.ValueString() != "" {
		g.Asset = &fianu_entities.CriteriaAssetScope{
			Type: db_vars.AssetType(asset.Type.ValueString()),
		}
	}

	for i, e := range exprs {
		raw := e.Expression.ValueString()
		// Pre-parse the user's pretty CEL into the canonical CEL form the
		// server's validator expects. The validator at
		// core/pkg/policies/service.go::validateCELExpressions runs
		// cel.CompileExpression on ExprSource, which requires the canonical
		// form (with $ prefixes + .(type) casts), not raw.
		parsed, err := cel.ParseExpression(raw)
		if err != nil {
			// Fall back to the raw form. Server-side Prepare will retry the
			// parse if Expr is set and ExprSource/Display are both empty;
			// that's the best we can do without failing the deploy on a
			// string we couldn't parse.
			rawCopy := raw
			g.Expressions = append(g.Expressions, fianu_entities.PolicyAssetGroupExpression{Seq: i + 1, Expr: &rawCopy})
			continue
		}
		g.Expressions = append(g.Expressions, fianu_entities.PolicyAssetGroupExpression{
			Seq:         i + 1,
			ExprSource:  parsed,
			ExprDisplay: raw,
		})
	}

	for _, idx := range indexes {
		// Go field names stay IndexID/IndexPath; HCL surface is id/path.
		g.Indexes = append(g.Indexes, fianu_entities.IndexReference{
			IndexID:   idx.ID.ValueString(),
			IndexPath: idx.Path.ValueString(),
		})
	}

	return g
}

// ToEntity converts the HCL criteria into the wire-side PolicyAssetGroup.
func (c *CriteriaModel) ToEntity() *fianu_entities.PolicyAssetGroup {
	if c == nil {
		return nil
	}
	g := AssetGroup(c.Asset, c.Expressions, c.Indexes)
	g.Name = c.Name.ValueString()
	g.Description = c.Description.ValueString()
	if cw := c.CombineWith.ValueString(); cw != "" {
		g.CombineWith = cw
	}
	return g
}
