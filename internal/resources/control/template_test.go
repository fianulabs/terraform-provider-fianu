// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package control_test

import (
	"encoding/json"
	"regexp"
	"testing"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccFianuControl_Template asserts the `template` section reaches the wire
// intact. Both JSONB fields are jsonencode-authored passthroughs, so the thing
// worth testing is that the bytes survive: the reporting service owns their
// shape and this provider must not reinterpret it.
func TestAccFianuControl_Template(t *testing.T) {
	stub := newConsoleStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigControlTemplate,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_control.templated", "detail.template.template_name", "SOC 2 access review"),
				),
			},
			{
				Config: testAccConfigControlTemplate,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got, _ := stub.capturedEntity.Load().(*fianu_entities.Control)
	if got == nil {
		t.Fatal("expected the stub to have captured a deployed entity, got nil")
	}
	if got.Detail.Template == nil {
		t.Fatal("detail.template was dropped; the section never reached the wire")
	}
	if got.Detail.Template.TemplateName != "SOC 2 access review" {
		t.Errorf("templateName = %q", got.Detail.Template.TemplateName)
	}

	var content map[string]any
	if err := json.Unmarshal(got.Detail.Template.TemplateContent, &content); err != nil {
		t.Fatalf("templateContent is not a JSON object on the wire: %v", err)
	}
	if content["mode"] != "raw" {
		t.Errorf("templateContent.mode = %v, want raw", content["mode"])
	}
	if content["source"] != "<h1>{{ .Control.Name }}</h1>" {
		t.Errorf("templateContent.source = %v", content["source"])
	}

	var snapshot map[string]any
	if err := json.Unmarshal(got.Detail.Template.SchemaSnapshot, &snapshot); err != nil {
		t.Fatalf("schemaSnapshot is not a JSON object on the wire: %v", err)
	}
	if snapshot["type"] != "object" {
		t.Errorf("schemaSnapshot.type = %v, want object", snapshot["type"])
	}
}

const testAccConfigControlTemplate = `
provider "fianu" {}

resource "fianu_control" "templated" {
  path = "test.control.templated"
  name = "Templated Control"

  detail = {
    full_name   = "Templated Control"
    display_key = "TMPL"

    template = {
      template_name = "SOC 2 access review"

      template_content = jsonencode({
        mode   = "raw"
        source = "<h1>{{ .Control.Name }}</h1>"
      })

      schema_snapshot = jsonencode({
        type = "object"
        properties = {
          findings = { type = "array" }
        }
      })
    }
  }
}
`

// TestAccFianuControl_TemplateOmitted pins the absent-vs-empty distinction.
// Detail.Template is a pointer because the server applies the template section
// only when it is non-nil; sending an empty object would make every deploy of
// every control claim to set a template.
func TestAccFianuControl_TemplateOmitted(t *testing.T) {
	stub := newConsoleStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigControlNoTemplate},
		},
	})

	got, _ := stub.capturedEntity.Load().(*fianu_entities.Control)
	if got == nil {
		t.Fatal("expected the stub to have captured a deployed entity, got nil")
	}
	if got.Detail.Template != nil {
		t.Errorf("detail.template = %+v, want nil when the block is absent", got.Detail.Template)
	}
}

const testAccConfigControlNoTemplate = `
provider "fianu" {}

resource "fianu_control" "templated" {
  path = "test.control.templated"
  name = "Templated Control"

  detail = {
    full_name   = "Templated Control"
    display_key = "TMPL"
  }
}
`

// TestAccFianuControl_RejectsMalformedTemplateContent pins the plan-time guard.
// A bad JSON string would otherwise be dropped in buildTemplate, deploying a
// control whose report renders empty.
func TestAccFianuControl_RejectsMalformedTemplateContent(t *testing.T) {
	stub := newConsoleStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigControlBadTemplate,
				ExpectError: regexp.MustCompile(`value is not valid JSON`),
			},
		},
	})
}

const testAccConfigControlBadTemplate = `
provider "fianu" {}

resource "fianu_control" "templated" {
  path = "test.control.templated"
  name = "Templated Control"

  detail = {
    full_name   = "Templated Control"
    display_key = "TMPL"

    template = {
      template_content = "{not json"
    }
  }
}
`
