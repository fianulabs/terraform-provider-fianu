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
const maxMeasureDepth = 5

// measureModelL5 is the leaf level — it's the only level without `children`,
// which terminates the recursion.
type measureModelL5 struct {
	Name        types.String `tfsdk:"name"`
	Type        types.String `tfsdk:"type"`
	Value       types.String `tfsdk:"value"`
	Description types.String `tfsdk:"description"`
}

type measureModelL4 struct {
	Name        types.String     `tfsdk:"name"`
	Type        types.String     `tfsdk:"type"`
	Value       types.String     `tfsdk:"value"`
	Description types.String     `tfsdk:"description"`
	Children    []measureModelL5 `tfsdk:"children"`
}

type measureModelL3 struct {
	Name        types.String     `tfsdk:"name"`
	Type        types.String     `tfsdk:"type"`
	Value       types.String     `tfsdk:"value"`
	Description types.String     `tfsdk:"description"`
	Children    []measureModelL4 `tfsdk:"children"`
}

type measureModelL2 struct {
	Name        types.String     `tfsdk:"name"`
	Type        types.String     `tfsdk:"type"`
	Value       types.String     `tfsdk:"value"`
	Description types.String     `tfsdk:"description"`
	Children    []measureModelL3 `tfsdk:"children"`
}

type measureModelL1 struct {
	Name        types.String     `tfsdk:"name"`
	Type        types.String     `tfsdk:"type"`
	Value       types.String     `tfsdk:"value"`
	Description types.String     `tfsdk:"description"`
	Children    []measureModelL2 `tfsdk:"children"`
}

// measureLeafFields are the fields every level of the measures tree carries.
// Returned as a fresh map so callers can mutate or augment per level.
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
				stringvalidator.OneOf("section", "metric"),
			},
		},
		"value": schema.StringAttribute{
			MarkdownDescription: "Value type for `metric` measures. One of `bool`, `number`, `string`, `array.string`. Ignored for `section` measures.",
			Optional:            true,
			Validators: []validator.String{
				stringvalidator.OneOf("bool", "number", "string", "array.string"),
			},
		},
		"description": schema.StringAttribute{
			MarkdownDescription: "Optional human-readable description shown in policy authoring UI.",
			Optional:            true,
		},
	}
}

// measuresAttribute returns the top-level `measures` ListNestedAttribute with
// five nested levels of `children`. The verbosity is the cost of static schema
// validation across the whole tree — Terraform diffs each leaf cleanly.
func measuresAttribute() schema.ListNestedAttribute {
	level5 := schema.NestedAttributeObject{Attributes: measureLeafFields()}

	l4Fields := measureLeafFields()
	l4Fields["children"] = schema.ListNestedAttribute{
		MarkdownDescription: "Nested measure children (level 5 — leaf).",
		Optional:            true,
		NestedObject:        level5,
	}
	level4 := schema.NestedAttributeObject{Attributes: l4Fields}

	l3Fields := measureLeafFields()
	l3Fields["children"] = schema.ListNestedAttribute{
		MarkdownDescription: "Nested measure children (level 4).",
		Optional:            true,
		NestedObject:        level4,
	}
	level3 := schema.NestedAttributeObject{Attributes: l3Fields}

	l2Fields := measureLeafFields()
	l2Fields["children"] = schema.ListNestedAttribute{
		MarkdownDescription: "Nested measure children (level 3).",
		Optional:            true,
		NestedObject:        level3,
	}
	level2 := schema.NestedAttributeObject{Attributes: l2Fields}

	l1Fields := measureLeafFields()
	l1Fields["children"] = schema.ListNestedAttribute{
		MarkdownDescription: "Nested measure children (level 2).",
		Optional:            true,
		NestedObject:        level2,
	}
	level1 := schema.NestedAttributeObject{Attributes: l1Fields}

	return schema.ListNestedAttribute{
		MarkdownDescription: "Hierarchical policy measure tree. Sections group related metrics; metrics carry a typed `value` (bool/number/string/array.string). Bounded to 5 levels of nesting.",
		Optional:            true,
		NestedObject:        level1,
	}
}

// buildMeasures converts the typed Terraform model back into the entity-side
// Measure structs the SDK expects. Each level recurses one shallower into the
// model.
func buildMeasures(in []measureModelL1) []fianu_entities.Measure {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.Measure, len(in))
	for i, m := range in {
		out[i] = fianu_entities.Measure{
			Name:        m.Name.ValueString(),
			Type:        m.Type.ValueString(),
			Value:       valueOrNil(m.Value),
			Description: stringPtr(m.Description),
			Children:    buildMeasuresL2(m.Children),
		}
	}
	return out
}

func buildMeasuresL2(in []measureModelL2) []fianu_entities.Measure {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.Measure, len(in))
	for i, m := range in {
		out[i] = fianu_entities.Measure{
			Name:        m.Name.ValueString(),
			Type:        m.Type.ValueString(),
			Value:       valueOrNil(m.Value),
			Description: stringPtr(m.Description),
			Children:    buildMeasuresL3(m.Children),
		}
	}
	return out
}

func buildMeasuresL3(in []measureModelL3) []fianu_entities.Measure {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.Measure, len(in))
	for i, m := range in {
		out[i] = fianu_entities.Measure{
			Name:        m.Name.ValueString(),
			Type:        m.Type.ValueString(),
			Value:       valueOrNil(m.Value),
			Description: stringPtr(m.Description),
			Children:    buildMeasuresL4(m.Children),
		}
	}
	return out
}

func buildMeasuresL4(in []measureModelL4) []fianu_entities.Measure {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.Measure, len(in))
	for i, m := range in {
		out[i] = fianu_entities.Measure{
			Name:        m.Name.ValueString(),
			Type:        m.Type.ValueString(),
			Value:       valueOrNil(m.Value),
			Description: stringPtr(m.Description),
			Children:    buildMeasuresL5(m.Children),
		}
	}
	return out
}

func buildMeasuresL5(in []measureModelL5) []fianu_entities.Measure {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.Measure, len(in))
	for i, m := range in {
		out[i] = fianu_entities.Measure{
			Name:        m.Name.ValueString(),
			Type:        m.Type.ValueString(),
			Value:       valueOrNil(m.Value),
			Description: stringPtr(m.Description),
		}
	}
	return out
}

// valueOrNil returns the string value or nil so the JSON marshaler emits
// nothing for unset value fields (e.g., on `section` measures).
func valueOrNil(v types.String) interface{} {
	if v.IsNull() || v.IsUnknown() {
		return nil
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
