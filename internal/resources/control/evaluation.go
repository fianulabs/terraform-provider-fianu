package control

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	pkgvariables "github.com/fianulabs/core/v2/external/pkg/variables"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// evaluationCaseModel is one entry in detail.evaluation. Each entry maps to a
// file in the on-disk control package: rule.rego, rule_test.rego, detail.py,
// display.py, report.py, input/*.json, data/*.json.
type evaluationCaseModel struct {
	Type    types.String `tfsdk:"type"`
	Engine  types.String `tfsdk:"engine"`
	Label   types.String `tfsdk:"label"`
	Enabled types.Bool   `tfsdk:"enabled"`
	Content types.String `tfsdk:"content"`
}

// evaluationAttribute is the HCL surface for the evaluation list. Users set
// `content` either via heredoc or `file("${path.module}/rule.rego")` — both
// work because Terraform resolves `file()` to the bytes before the provider
// sees the value.
func evaluationAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "Evaluation cases — the rego/python/json files that drive control evaluation. Each case mirrors a file in the on-disk control package: `rule.rego`, `rule_test.rego`, `detail.py`, `display.py`, `report.py`, `input/*.json`, `data/*.json`.",
		Optional:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"type": schema.StringAttribute{
					MarkdownDescription: "Case type. One of `rule`, `rule_test`, `detail`, `display`, `report`, `input`, `data`. Matches the `pkgvariables.ControlResource` constants in core.",
					Required:            true,
					Validators: []validator.String{
						stringvalidator.OneOf(
							string(pkgvariables.CRRule),
							string(pkgvariables.CRRuleTest),
							string(pkgvariables.CRDetail),
							string(pkgvariables.CRDisplay),
							string(pkgvariables.CRReport),
							string(pkgvariables.CRInput),
							string(pkgvariables.CRData),
						),
					},
				},
				"engine": schema.StringAttribute{
					MarkdownDescription: "Evaluation engine for `rule` cases (typically `opa`). Optional; ignored for non-rule cases.",
					Optional:            true,
				},
				"label": schema.StringAttribute{
					MarkdownDescription: "Optional human-readable label (typically the source filename, e.g., `rule.rego`).",
					Optional:            true,
				},
				"enabled": schema.BoolAttribute{
					MarkdownDescription: "Whether this case participates in evaluation. Defaults to true when omitted.",
					Optional:            true,
				},
				"content": schema.StringAttribute{
					MarkdownDescription: "Case body — rego/python/JSON content. Use `file(\"${path.module}/rule.rego\")` to load from disk, or an HCL heredoc for inline content.",
					Required:            true,
				},
			},
		},
	}
}

// buildEvaluationCases translates HCL-side evaluation entries into the
// entity-side Case slice. The on-the-wire encoding is raw bytes; the server
// accepts both raw and base64 (see fianu_entities.Case.UnmarshalJSON), so we
// pick raw to keep diffs readable in any captured logs.
func buildEvaluationCases(in []evaluationCaseModel) []fianu_entities.Case {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.Case, len(in))
	for i, c := range in {
		body := fianu_entities.CaseBody{
			Type:  pkgvariables.ControlResource(c.Type.ValueString()),
			Label: c.Label.ValueString(),
		}
		if !c.Enabled.IsNull() && !c.Enabled.IsUnknown() {
			body.Enabled = c.Enabled.ValueBool()
		} else {
			body.Enabled = true
		}
		out[i] = fianu_entities.Case{
			CaseBody: body,
			Detail:   []byte(c.Content.ValueString()),
		}
	}
	return out
}
