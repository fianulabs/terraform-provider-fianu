// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package control

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Bounded recursion: terraform-plugin-framework cannot express truly
// self-referential nested attributes, so we manually unroll five levels of
// `measures.children`. Production controls in official-controls/ go no deeper
// than 3 levels (vulnerabilities → critical → maximum), so 5 leaves headroom
// without the schema explosion that comes from going further.
//
// If a tree ever needs more depth, the user can split it across multiple
// `policy_template.measures` entries or compose via Terraform `locals`.

// measureNode is the shape every level of the model exposes. Defining it
// once means the conversion to fianu_entities.Measure lives in a single
// recursive helper instead of five copy-pasted functions.
type measureNode interface {
	core() (name, typ, valueStr string, descPtr *string)
	childMeasures() []fianu_entities.Measure
}

func convertMeasureNode(n measureNode) fianu_entities.Measure {
	name, typ, val, desc := n.core()
	m := fianu_entities.Measure{
		Name:        name,
		Type:        typ,
		Description: desc,
		Children:    n.childMeasures(),
	}
	if val != "" {
		m.Value = val
	}
	return m
}

// measureModelLeaf carries the four fields every level shares — extracted so
// each level only declares the children type that varies.
type measureModelLeaf struct {
	Name        types.String `tfsdk:"name"`
	Type        types.String `tfsdk:"type"`
	Value       types.String `tfsdk:"value"`
	Description types.String `tfsdk:"description"`
}

func (m measureModelLeaf) core() (string, string, string, *string) {
	return m.Name.ValueString(), m.Type.ValueString(), valueStringOrEmpty(m.Value), stringPtr(m.Description)
}

type measureModelL5 struct{ measureModelLeaf }

func (m measureModelL5) childMeasures() []fianu_entities.Measure { return nil }

type measureModelL4 struct {
	measureModelLeaf
	Children []measureModelL5 `tfsdk:"children"`
}

func (m measureModelL4) childMeasures() []fianu_entities.Measure { return walk(m.Children) }

type measureModelL3 struct {
	measureModelLeaf
	Children []measureModelL4 `tfsdk:"children"`
}

func (m measureModelL3) childMeasures() []fianu_entities.Measure { return walk(m.Children) }

type measureModelL2 struct {
	measureModelLeaf
	Children []measureModelL3 `tfsdk:"children"`
}

func (m measureModelL2) childMeasures() []fianu_entities.Measure { return walk(m.Children) }

type measureModelL1 struct {
	measureModelLeaf
	Children []measureModelL2 `tfsdk:"children"`
}

func (m measureModelL1) childMeasures() []fianu_entities.Measure { return walk(m.Children) }

// walk converts a slice of any measure level into the entity-side slice.
// Generic over the level type; one function replaces the five buildMeasures*
// helpers from the previous revision.
func walk[T measureNode](in []T) []fianu_entities.Measure {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.Measure, len(in))
	for i, m := range in {
		out[i] = convertMeasureNode(m)
	}
	return out
}

// buildMeasures is the public entry point used by resource.go. Stays a thin
// wrapper so callers don't have to know about the level types.
func buildMeasures(in []measureModelL1) []fianu_entities.Measure {
	return walk(in)
}

// measureLeafFields returns the fields every level of the measures tree
// carries. The OneOf validators reference the typed enum constants in core
// (entities.AllMeasureTypes / AllMeasureValues) so the valid set is one
// source of truth.
func measureLeafFields() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"name": schema.StringAttribute{
			MarkdownDescription: "Measure name. Used as the path key when policies reference this measure (e.g., `vulnerabilities.critical.maximum`).",
			Required:            true,
		},
		"type": schema.StringAttribute{
			MarkdownDescription: "Either `section` (a grouping with `children`) or `metric` (a leaf carrying a `value`).",
			Required:            true,
			Validators: []validator.String{
				stringvalidator.OneOf(fianu_entities.AllMeasureTypes()...),
			},
		},
		"value": schema.StringAttribute{
			MarkdownDescription: "Value type for `metric` measures. One of `bool`, `number`, `string`, `array.string`. Ignored for `section` measures.",
			Optional:            true,
			Validators: []validator.String{
				stringvalidator.OneOf(fianu_entities.AllMeasureValues()...),
			},
		},
		"description": schema.StringAttribute{
			MarkdownDescription: "Optional human-readable description shown in policy authoring UI.",
			Optional:            true,
		},
	}
}

// measuresAttribute returns the top-level `measures` ListNestedAttribute
// composed from the same leaf fields at each of five depths.
func measuresAttribute() schema.ListNestedAttribute {
	level5 := schema.NestedAttributeObject{Attributes: measureLeafFields()}
	level4 := schema.NestedAttributeObject{Attributes: withChildren(measureLeafFields(), level5, "level 5 — leaf")}
	level3 := schema.NestedAttributeObject{Attributes: withChildren(measureLeafFields(), level4, "level 4")}
	level2 := schema.NestedAttributeObject{Attributes: withChildren(measureLeafFields(), level3, "level 3")}
	level1 := schema.NestedAttributeObject{Attributes: withChildren(measureLeafFields(), level2, "level 2")}

	return schema.ListNestedAttribute{
		MarkdownDescription: "Hierarchical policy measure tree. Sections group related metrics; metrics carry a typed `value` (bool/number/string/array.string). Bounded to 5 levels of nesting.",
		Optional:            true,
		NestedObject:        level1,
	}
}

// withChildren attaches a `children` ListNestedAttribute at the next-deeper
// level. Pulling this out collapses five copies of the same wiring into one.
func withChildren(fields map[string]schema.Attribute, child schema.NestedAttributeObject, label string) map[string]schema.Attribute {
	fields["children"] = schema.ListNestedAttribute{
		MarkdownDescription: "Nested measure children (" + label + ").",
		Optional:            true,
		NestedObject:        child,
	}
	return fields
}

// valueStringOrEmpty returns the string value or "" so callers can quickly
// distinguish "set" from "unset" without needing the types.String wrapper.
func valueStringOrEmpty(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

// stringPtr returns a pointer to the underlying string when set, nil otherwise.
// Matches the pattern Detail.Description uses on ControlInfo.
func stringPtr(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}
