// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package reporttemplate_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	transportv1 "github.com/fianulabs/core/v2/external/transport/http/v1"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/fianulabs/terraform-provider-fianu/internal/provider"
)

// TestAccFianuReportTemplate_Minimal covers identity only, and pins the
// composite ID prefix: EntityTypeReportTemplate is the string "template", not
// "report_template", so that is what the id and import path must use.
func TestAccFianuReportTemplate_Minimal(t *testing.T) {
	stub := newTemplateStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigTemplateMinimal,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_report_template.soc2", "id", "template/test.template.soc2"),
					resource.TestCheckResourceAttr("fianu_report_template.soc2", "uuid", "test-template-uuid"),
					resource.TestCheckResourceAttr("fianu_report_template.soc2", "version.status", "active"),
				),
			},
			{
				Config: testAccConfigTemplateMinimal,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Type != db_vars.EntityTypeReportTemplate {
		t.Errorf("entity type = %q, want %q", got.Type, db_vars.EntityTypeReportTemplate)
	}
	if got.Path != "test.template.soc2" {
		t.Errorf("path = %q", got.Path)
	}
}

const testAccConfigTemplateMinimal = `
provider "fianu" {}

resource "fianu_report_template" "soc2" {
  path = "test.template.soc2"
  name = "SOC 2 Report"

  detail = {
    description = "Annual SOC 2 Type II evidence package"
  }
}
`

// TestAccFianuReportTemplate_FullSpec asserts layout_config survives as a JSON
// object and output_formats round-trips.
func TestAccFianuReportTemplate_FullSpec(t *testing.T) {
	stub := newTemplateStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigTemplateFull},
			{
				Config: testAccConfigTemplateFull,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if len(got.Detail.OutputFormats) != 2 {
		t.Fatalf("outputFormats = %v, want 2 entries", got.Detail.OutputFormats)
	}
	if got.Detail.LayoutConfig == nil {
		t.Fatal("layoutConfig was dropped")
	}
	var layout map[string]any
	if err := json.Unmarshal(got.Detail.LayoutConfig, &layout); err != nil {
		t.Fatalf("layoutConfig is not a JSON object on the wire: %v (raw: %s)", err, got.Detail.LayoutConfig)
	}
	sections, ok := layout["sections"].([]any)
	if !ok || len(sections) != 2 {
		t.Errorf("layoutConfig.sections = %v, want 2 entries", layout["sections"])
	}
	if got.Detail.Description == nil || *got.Detail.Description != "Annual SOC 2 Type II evidence package" {
		t.Errorf("description = %v", got.Detail.Description)
	}
}

const testAccConfigTemplateFull = `
provider "fianu" {}

resource "fianu_report_template" "soc2" {
  path = "test.template.soc2"
  name = "SOC 2 Report"

  detail = {
    description    = "Annual SOC 2 Type II evidence package"
    output_formats = ["pdf", "html"]

    layout_config = jsonencode({
      header = "soc2.header"
      footer = "soc2.footer"
      sections = [
        { title = "Access Control", controls = ["okta.mfa.enforced"] },
        { title = "Change Management", controls = ["github.pr.approved"] },
      ]
    })
  }
}
`

// TestAccFianuReportTemplate_Update proves an in-place change round-trips.
func TestAccFianuReportTemplate_Update(t *testing.T) {
	stub := newTemplateStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigTemplateMinimal},
			{
				Config: testAccConfigTemplateUpdated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_report_template.soc2", "detail.description", "SOC 2 Type I"),
					resource.TestCheckResourceAttr("fianu_report_template.soc2", "detail.output_formats.0", "json"),
				),
			},
		},
	})

	got := stub.captured(t)
	if got.Detail.Description == nil || *got.Detail.Description != "SOC 2 Type I" {
		t.Errorf("after update, description = %v", got.Detail.Description)
	}
	if stub.deployHits.Load() < 2 {
		t.Errorf("deploy hits = %d, want at least 2", stub.deployHits.Load())
	}
}

const testAccConfigTemplateUpdated = `
provider "fianu" {}

resource "fianu_report_template" "soc2" {
  path = "test.template.soc2"
  name = "SOC 2 Report"

  detail = {
    description    = "SOC 2 Type I"
    output_formats = ["json"]
  }
}
`

// TestAccFianuReportTemplate_Import proves the composite ID round-trips.
func TestAccFianuReportTemplate_Import(t *testing.T) {
	stub := newTemplateStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigTemplateMinimal},
			{
				Config:                  testAccConfigTemplateMinimal,
				ResourceName:            "fianu_report_template.soc2",
				ImportState:             true,
				ImportStateId:           "template/test.template.soc2",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"detail"},
			},
		},
	})
}

// TestAccFianuReportTemplate_RejectsMalformedLayout pins the plan-time guard.
// A bad layout_config would otherwise deploy as an empty layout and render an
// empty report rather than failing.
func TestAccFianuReportTemplate_RejectsMalformedLayout(t *testing.T) {
	stub := newTemplateStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigTemplateBadLayout,
				ExpectError: regexp.MustCompile(`layout_config is not a JSON object`),
			},
		},
	})
}

const testAccConfigTemplateBadLayout = `
provider "fianu" {}

resource "fianu_report_template" "soc2" {
  path = "test.template.soc2"
  name = "SOC 2 Report"

  detail = {
    layout_config = "sections: ["
  }
}
`

// TestAccFianuReportTemplate_RejectsUnknownOutputFormat proves the format
// allowlist is wired in.
func TestAccFianuReportTemplate_RejectsUnknownOutputFormat(t *testing.T) {
	stub := newTemplateStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigTemplateBadFormat,
				ExpectError: regexp.MustCompile(`(?s)value must be one of`),
			},
		},
	})
}

const testAccConfigTemplateBadFormat = `
provider "fianu" {}

resource "fianu_report_template" "soc2" {
  path = "test.template.soc2"
  name = "SOC 2 Report"

  detail = {
    output_formats = ["docx"]
  }
}
`

func setEnv(t *testing.T, stub *templateStub) {
	t.Helper()
	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")
}

func protoV6Factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"fianu": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

// templateStub fakes Console for the report template resource.
type templateStub struct {
	server       *httptest.Server
	deployHits   atomic.Int32
	archiveHits  atomic.Int32
	archivedPath atomic.Value // string
	stored       atomic.Value // *transportv1.DeployEntityFileResponse
	capturedVal  atomic.Value // *fianu_entities.ReportTemplate
	lastCaptured atomic.Value // *fianu_entities.ReportTemplate, never cleared
}

func (s *templateStub) captured(t *testing.T) *fianu_entities.ReportTemplate {
	t.Helper()
	e, _ := s.lastCaptured.Load().(*fianu_entities.ReportTemplate)
	if e == nil {
		t.Fatal("no report template captured on the deploy route")
	}
	return e
}

func newTemplateStub(t *testing.T) *templateStub {
	t.Helper()
	stub := &templateStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/entities/artifacts/deploy", func(w http.ResponseWriter, r *http.Request) {
		stub.deployHits.Add(1)
		req, raw := decodeDeployRequest(r)

		var e fianu_entities.ReportTemplate
		if err := json.Unmarshal(raw, &e); err == nil {
			stub.capturedVal.Store(&e)
			stub.lastCaptured.Store(&e)
		}

		// The real endpoint's allowlist is keyed on General.entityType.
		if req.General.EntityType == nil || *req.General.EntityType != string(db_vars.EntityTypeReportTemplate) {
			http.Error(w, `{"error":"unsupported entity type"}`, http.StatusBadRequest)
			return
		}

		action := "created"
		if prior, _ := stub.stored.Load().(*transportv1.DeployEntityFileResponse); prior != nil {
			if prior.Metadata != nil && prior.Metadata.ContentHash == r.Header.Get("X-Fianu-CI-System-Hash") {
				action = "skipped"
			} else {
				action = "updated"
			}
		}

		entityPath := ""
		if req.General.Path != nil {
			entityPath = *req.General.Path
		}
		resp := &transportv1.DeployEntityFileResponse{
			Message: "ok",
			Metadata: &transportv1.DeploymentMetadata{
				Action:      action,
				ContentHash: r.Header.Get("X-Fianu-CI-System-Hash"),
				EntityID:    "test-template-uuid",
				Path:        entityPath,
				Name:        e.Name,
				Version:     "1",
				EntityType:  string(db_vars.EntityTypeReportTemplate),
			},
		}
		stub.stored.Store(resp)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/entities/templates/", func(w http.ResponseWriter, r *http.Request) {
		captured, _ := stub.capturedVal.Load().(*fianu_entities.ReportTemplate)
		if captured == nil {
			http.NotFound(w, r)
			return
		}
		out := *captured
		out.UUID = "test-template-uuid"
		out.Type = db_vars.EntityTypeReportTemplate
		out.Version.Semantic = "1"
		out.Version.UUID = "version-uuid"
		out.Version.Status = "active"
		out.Version.State = "published"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("/api/entities/archive/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		stub.archiveHits.Add(1)
		stub.archivedPath.Store(r.URL.Path)
		stub.capturedVal.Store((*fianu_entities.ReportTemplate)(nil))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"archived"}`))
	})

	stub.server = httptest.NewServer(mux)
	return stub
}

// decodeDeployRequest parses the multipart deploy request into the General
// envelope plus the raw entity JSON.
func decodeDeployRequest(r *http.Request) (transportv1.DeployEntityFileRequest, []byte) {
	var req transportv1.DeployEntityFileRequest
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return req, nil
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return req, nil
	}
	if vals, ok := r.MultipartForm.Value["payload"]; ok && len(vals) > 0 {
		_ = json.Unmarshal([]byte(vals[0]), &req)
	}
	fileHeaders, ok := r.MultipartForm.File["file"]
	if !ok || len(fileHeaders) == 0 {
		return req, nil
	}
	fh, err := fileHeaders[0].Open()
	if err != nil {
		return req, nil
	}
	defer fh.Close()
	raw, err := io.ReadAll(fh)
	if err != nil {
		return req, nil
	}
	return req, raw
}
