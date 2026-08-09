// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package control

import (
	"encoding/json"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// templateModel mirrors entities.EntityTemplateDetail — the `template` section
// of the on-disk control package, stored as satellite data in entity_templates.
//
// Both JSONB fields are authored as `jsonencode({...})` strings rather than
// typed nested attributes. `templateContent` is a TemplateSpec with two modes
// (a structured "wizard" block tree and a "raw" Go template passthrough) whose
// shape is owned by the reporting service, and `schemaSnapshot` is a JSON
// Schema. Modelling either in HCL would pin a schema this provider does not own
// and would have to chase.
type templateModel struct {
	TemplateName    types.String `tfsdk:"template_name"`
	TemplateContent types.String `tfsdk:"template_content"`
	SchemaSnapshot  types.String `tfsdk:"schema_snapshot"`
}

func templateAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Report template for this control — the `template` section of the on-disk package. Both JSON attributes are authored with `jsonencode({...})`; their shapes are owned by the reporting service, so they are passed through rather than modelled here.",
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"template_name": schema.StringAttribute{
				MarkdownDescription: "Optional human-readable label for the template.",
				Optional:            true,
			},
			"template_content": schema.StringAttribute{
				MarkdownDescription: "TemplateSpec object as JSON. Two modes: `{ mode = \"wizard\", ... }` for structured block instructions the backend compiles to a Go template, or `{ mode = \"raw\", ... }` for passthrough Go `html/template` source. Author with `jsonencode({...})`, or `jsondecode(file(\"...\"))` re-encoded if the spec lives on disk.",
				Required:            true,
			},
			"schema_snapshot": schema.StringAttribute{
				MarkdownDescription: "Cached JSON Schema produced by running `report.py` against test fixtures, so the template editor does not re-execute it on every load. Optional — the server regenerates it when absent.",
				Optional:            true,
			},
		},
	}
}

// templateValidationDiags reports a malformed jsonencode at plan time. Without
// it a typo'd template_content is dropped by buildTemplate and the control
// deploys with no template at all, which surfaces much later as a report that
// renders empty.
func templateValidationDiags(root path.Path, in *templateModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if in == nil {
		return diags
	}

	check := func(p path.Path, v types.String) {
		if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
			return
		}
		if !json.Valid([]byte(v.ValueString())) {
			diags.AddAttributeError(p, "value is not valid JSON",
				"Author this attribute with jsonencode({...}).")
		}
	}

	check(root.AtName("template_content"), in.TemplateContent)
	check(root.AtName("schema_snapshot"), in.SchemaSnapshot)
	return diags
}

// buildTemplate converts the HCL model to the wire shape. Returns nil for an
// absent template: Detail.Template is a pointer because absent and empty differ
// — the server applies SectionTemplate only when it is non-nil, so sending an
// empty object would claim to set a template on every deploy.
func buildTemplate(in *templateModel) *fianu_entities.EntityTemplateDetail {
	if in == nil {
		return nil
	}
	return &fianu_entities.EntityTemplateDetail{
		TemplateName:    in.TemplateName.ValueString(),
		TemplateContent: rawJSON(in.TemplateContent),
		SchemaSnapshot:  rawJSON(in.SchemaSnapshot),
	}
}

// rawJSON returns nil for absent or invalid input. templateValidationDiags has
// already reported a parse failure with an attribute path by the time this
// runs, so returning nil here cannot swallow the error.
func rawJSON(v types.String) json.RawMessage {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}
	b := []byte(v.ValueString())
	if !json.Valid(b) {
		return nil
	}
	return json.RawMessage(b)
}
