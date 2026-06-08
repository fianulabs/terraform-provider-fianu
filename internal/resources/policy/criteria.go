// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package policy

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
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
//
// The three input shapes (mutually exclusive — server-side IsValid enforces):
//   - asset + expressions   → server spawns a private content-addressed index
//   - indexes (no asset)    → references an existing index by id or path
//   - asset only (no exprs) → unscoped variation; server links the default
//     index for that asset type
type criteriaModel struct {
	Name        types.String            `tfsdk:"name"`
	Description types.String            `tfsdk:"description"`
	CombineWith types.String            `tfsdk:"combine_with"`
	Asset       *criteriaAssetModel     `tfsdk:"asset"`
	Expressions []expressionModel       `tfsdk:"expressions"`
	Indexes     []criteriaIndexRefModel `tfsdk:"indexes"`
}

type criteriaAssetModel struct {
	Type types.String `tfsdk:"type"`
}

type criteriaIndexRefModel struct {
	ID   types.String `tfsdk:"id"`
	Path types.String `tfsdk:"path"`
}

type expressionModel struct {
	Expression types.String `tfsdk:"expression"`
}

func criteriaAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Asset group criteria. Restricts this variation to assets matching either a set of CEL expressions or one or more existing indexes. When omitted, the variation applies to every asset in the policy's scope.",
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
			"asset": schema.SingleNestedAttribute{
				MarkdownDescription: "Per-criteria asset binding. Required when `expressions` are supplied OR when the criteria is unscoped (no expressions and no indexes). Omit when `indexes` is set — the linked index already carries the asset type.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						MarkdownDescription: "Abstract asset type (e.g., `repository`, `application`, `module`). Built-ins are listed in the Console; orgs can register additional abstract asset types.",
						Required:            true,
					},
				},
			},
			"expressions": schema.ListNestedAttribute{
				MarkdownDescription: "CEL expressions evaluated per-asset. Uses Fianu's CEL dialect — combine clauses inside a single expression with `&&`/`||`; multiple list entries are only needed when mixing OR semantics across separate predicates via `combine_with`. Mutually exclusive with `indexes`.",
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
	// Provider boundary: convert types.String → variables.AssetType once
	// here. Internal Go callers downstream see the typed value with no
	// further casts.
	if c.Asset != nil && c.Asset.Type.ValueString() != "" {
		g.Asset = &fianu_entities.CriteriaAssetScope{
			Type: db_vars.AssetType(c.Asset.Type.ValueString()),
		}
	}
	if len(c.Expressions) > 0 {
		g.Expressions = make([]fianu_entities.PolicyAssetGroupExpression, len(c.Expressions))
		for i, e := range c.Expressions {
			raw := e.Expression.ValueString()
			// Pre-parse the user's pretty CEL into the canonical CEL form
			// the server's validator expects. The validator at
			// core/pkg/policies/service.go::validateCELExpressions runs
			// cel.CompileExpression on ExprSource, which requires the
			// canonical form (with $ prefixes + .(type) casts), not raw.
			parsed, err := cel.ParseExpression(raw)
			if err != nil {
				// Fall back to the raw form. Server-side Prepare will
				// retry the parse if Expr is set and ExprSource/Display
				// are both empty; that's the best we can do without
				// failing the deploy on a string we couldn't parse.
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
	if len(c.Indexes) > 0 {
		g.Indexes = make([]fianu_entities.IndexReference, 0, len(c.Indexes))
		for _, idx := range c.Indexes {
			// Go field names stay IndexID/IndexPath; HCL surface is id/path.
			g.Indexes = append(g.Indexes, fianu_entities.IndexReference{
				IndexID:   idx.ID.ValueString(),
				IndexPath: idx.Path.ValueString(),
			})
		}
	}
	return g
}
