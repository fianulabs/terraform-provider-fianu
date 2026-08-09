// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package base

import (
	"context"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// CriteriaShapeValidator returns an object validator that enforces the
// three-shape rule the Fianu server applies in
// `core/external/db/types/fianu/entities/policy.go::PolicyAssetGroup.IsValid`:
//
//   - `asset` + `expressions`         — inline criteria-as-code (server spawns a private index)
//   - `indexes` only (no `asset`)     — references existing index entities
//   - `asset` only                    — unscoped variation (server links the default index)
//
// `expressions` and `indexes` set together is rejected;
// `expressions` without `asset.type` is rejected; an entirely empty criteria
// (no `expressions`, no `indexes`, no `asset.type`) is rejected.
//
// Parity strategy: the validator builds a minimal `*fianu_entities.PolicyAssetGroup`
// from the framework's `attr.Value` tree and calls `.IsValid()` directly. The
// server's error message flows through to the Terraform diagnostic verbatim,
// so plan-time and apply-time text match.
//
// Skips validation when any criteria field is unknown (typical when criteria
// references a resource attribute computed later in the graph, e.g.,
// `indexes = [{ path = fianu_index.foo.path }]` where `fianu_index.foo` is
// being created in the same plan). The server will catch any residual
// invalid shape at apply time.
//
// Use on the `criteria` `SingleNestedAttribute` directly, and on
// `pods[*].matching[*]` via `listvalidator.ValueObjectsAre`.
func CriteriaShapeValidator() validator.Object {
	return criteriaShapeValidator{}
}

type criteriaShapeValidator struct{}

func (criteriaShapeValidator) Description(_ context.Context) string {
	return "validates the criteria shape against the server's PolicyAssetGroup.IsValid"
}

func (criteriaShapeValidator) MarkdownDescription(ctx context.Context) string {
	return "Validates the `criteria` block against the same rules the Fianu server enforces " +
		"in `PolicyAssetGroup.IsValid` (`core/external/db/types/fianu/entities/policy.go`): " +
		"one of `asset` + `expressions`, `indexes` only, or `asset` only; setting both " +
		"`expressions` and `indexes` is rejected, and `expressions`-only / unscoped variations " +
		"must carry `asset.type`."
}

func (criteriaShapeValidator) ValidateObject(ctx context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	g, skip := buildAssetGroup(req.ConfigValue)
	if skip {
		return
	}

	if err := g.IsValid(); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"invalid criteria shape",
			err.Error(),
		)
	}
}

// buildAssetGroup translates the framework's `types.Object` representation
// of a criteria block into a wire-shape `*PolicyAssetGroup` suitable for
// `IsValid()`. Returns skip=true if any field is unknown — we can't
// validate criteria whose contents depend on resources created later in
// the graph.
func buildAssetGroup(obj types.Object) (*fianu_entities.PolicyAssetGroup, bool) {
	attrs := obj.Attributes()
	g := &fianu_entities.PolicyAssetGroup{}

	if a, ok := attrs["asset"]; ok && !a.IsNull() {
		if a.IsUnknown() {
			return nil, true
		}
		assetObj, ok := a.(types.Object)
		if !ok {
			return g, false
		}
		t := assetObj.Attributes()["type"]
		if t != nil && t.IsUnknown() {
			return nil, true
		}
		if s, ok := t.(types.String); ok && !s.IsNull() && s.ValueString() != "" {
			g.Asset = &fianu_entities.CriteriaAssetScope{
				Type: db_vars.AssetType(s.ValueString()),
			}
		}
	}

	// expressions[] — count is what IsValid checks; presence is enough.
	if e, ok := attrs["expressions"]; ok && !e.IsNull() {
		if e.IsUnknown() {
			return nil, true
		}
		if list, ok := e.(types.List); ok {
			if n := len(list.Elements()); n > 0 {
				g.Expressions = make([]fianu_entities.PolicyAssetGroupExpression, n)
			}
		}
	}

	// indexes[] — IsValid iterates each entry and calls ref.IsValid(), so
	// populate IndexID/IndexPath per element to catch per-entry issues
	// (missing both, both set, invalid UUID).
	if i, ok := attrs["indexes"]; ok && !i.IsNull() {
		if i.IsUnknown() {
			return nil, true
		}
		list, ok := i.(types.List)
		if !ok {
			return g, false
		}
		elems := list.Elements()
		if len(elems) > 0 {
			g.Indexes = make([]fianu_entities.IndexReference, 0, len(elems))
			for _, elem := range elems {
				if elem.IsNull() {
					continue
				}
				if elem.IsUnknown() {
					return nil, true
				}
				eObj, ok := elem.(types.Object)
				if !ok {
					return g, false
				}
				idVal, idSkip := stringAttr(eObj.Attributes()["id"])
				if idSkip {
					return nil, true
				}
				pathVal, pathSkip := stringAttr(eObj.Attributes()["path"])
				if pathSkip {
					return nil, true
				}
				g.Indexes = append(g.Indexes, fianu_entities.IndexReference{
					IndexID:   idVal,
					IndexPath: pathVal,
				})
			}
		}
	}

	return g, false
}

// stringAttr extracts a string from an attr.Value, returning skip=true when
// the value is unknown.
func stringAttr(v attr.Value) (val string, skip bool) {
	if v == nil || v.IsNull() {
		return "", false
	}
	if v.IsUnknown() {
		return "", true
	}
	s, ok := v.(types.String)
	if !ok {
		return "", false
	}
	return s.ValueString(), false
}
