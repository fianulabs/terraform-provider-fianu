// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package form_test

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

// TestAccFianuForm_Minimal covers the smallest form the server accepts:
// identity plus one element, which Form.Validate requires.
func TestAccFianuForm_Minimal(t *testing.T) {
	stub := newFormStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigFormMinimal,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_form.review", "id", "form/test.form.review"),
					resource.TestCheckResourceAttr("fianu_form.review", "uuid", "test-form-uuid"),
					resource.TestCheckResourceAttr("fianu_form.review", "detail.elements.#", "1"),
					// Refetch-after-deploy: these come from the read, not the
					// deploy metadata, so their presence proves applyPlan's
					// second call landed.
					resource.TestCheckResourceAttr("fianu_form.review", "version.status", "active"),
					resource.TestCheckResourceAttr("fianu_form.review", "version.state", "published"),
				),
			},
			{
				Config: testAccConfigFormMinimal,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Type != db_vars.EntityTypeForm {
		t.Errorf("entity type = %q, want form", got.Type)
	}
	if got.Path != "test.form.review" {
		t.Errorf("path = %q, want test.form.review", got.Path)
	}
	if len(got.Detail.Elements) != 1 {
		t.Fatalf("elements = %d, want 1", len(got.Detail.Elements))
	}
	if got.Detail.Elements[0].Name != "Did you review the change?" {
		t.Errorf("elements[0].name = %q", got.Detail.Elements[0].Name)
	}
}

const testAccConfigFormMinimal = `
provider "fianu" {}

resource "fianu_form" "review" {
  path = "test.form.review"
  name = "Change Review"

  detail = {
    elements = [
      {
        name = "Did you review the change?"
        type = "boolean"
        options = jsonencode({
          values = { yes = "Yes", no = "No" }
        })
      },
    ]
  }
}
`

// TestAccFianuForm_DeploysActive pins the lifecycle decision. The form deployer
// defaults an unspecified form to draft/inactive to match the console's create
// endpoint, and the form read filters to active — so if buildEntity ever stops
// setting these, every fianu_form would 404 on its own next Read and silently
// vanish from state. This asserts on the wire, not on the stub's echo.
func TestAccFianuForm_DeploysActive(t *testing.T) {
	stub := newFormStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigFormMinimal},
		},
	})

	got := stub.captured(t)
	if got.Version.Status != db_vars.EntityStatusActive {
		t.Errorf("deployed version.status = %q, want active — a draft form is invisible to its own Read", got.Version.Status)
	}
	if got.Version.State != db_vars.EntityStatePublished {
		t.Errorf("deployed version.state = %q, want published", got.Version.State)
	}
}

// TestAccFianuForm_FullSpec exercises every detail attribute across every
// supported element type, and asserts the per-type `options` payloads survive
// the jsonencode round-trip — the server validates options against the element
// type, so a dropped key is the failure that matters.
func TestAccFianuForm_FullSpec(t *testing.T) {
	stub := newFormStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigFormFull},
			{
				Config: testAccConfigFormFull,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Detail.DisplayKey != "vendor-review" {
		t.Errorf("displayKey = %q, want vendor-review", got.Detail.DisplayKey)
	}
	if got.Detail.Description == nil || *got.Detail.Description != "Annual vendor security review" {
		t.Errorf("description = %v", got.Detail.Description)
	}
	if len(got.Detail.Elements) != 4 {
		t.Fatalf("elements = %d, want 4", len(got.Detail.Elements))
	}

	// Codes are positional and assigned by the provider from list order. The
	// server re-derives them the same way, so a mismatch here means answers
	// would bind to the wrong question.
	for i, e := range got.Detail.Elements {
		if e.Code != i {
			t.Errorf("elements[%d].code = %d, want %d", i, e.Code, i)
		}
	}

	text := got.Detail.Elements[0]
	if text.Type != db_vars.TextInput {
		t.Errorf("elements[0].type = %q, want text", text.Type)
	}
	if !text.Required {
		t.Error("elements[0].required = false, want true")
	}
	if text.Description != "Full legal name" {
		t.Errorf("elements[0].description = %q", text.Description)
	}
	if text.Options == nil {
		t.Fatal("elements[0].options was dropped; the jsonencode string did not reach the wire")
	}
	if text.Options["validation"] != true {
		t.Errorf("elements[0].options.validation = %v, want true", text.Options["validation"])
	}
	if text.Options["expression"] != "^[A-Za-z ]+$" {
		t.Errorf("elements[0].options.expression = %v", text.Options["expression"])
	}

	blob := got.Detail.Elements[1]
	if blob.Type != db_vars.MultiLineInput {
		t.Errorf("elements[1].type = %q, want blob", blob.Type)
	}
	if blob.Options["placeholder"] != "Describe the scope" {
		t.Errorf("elements[1].options.placeholder = %v", blob.Options["placeholder"])
	}

	radio := got.Detail.Elements[2]
	if radio.Type != db_vars.RadioButton {
		t.Errorf("elements[2].type = %q, want radio", radio.Type)
	}
	values, ok := radio.Options["values"].(map[string]any)
	if !ok {
		t.Fatalf("elements[2].options.values = %T, want a JSON object", radio.Options["values"])
	}
	if values["high"] != "High" {
		t.Errorf("elements[2].options.values.high = %v, want High", values["high"])
	}

	checkbox := got.Detail.Elements[3]
	if checkbox.Type != db_vars.Checkboxes {
		t.Errorf("elements[3].type = %q, want checkbox", checkbox.Type)
	}
	// An element with no options must stay nil, not become an empty map: the
	// server's ValidateOptions branches on nil.
	if checkbox.Options != nil {
		t.Errorf("elements[3].options = %v, want nil for an unset field", checkbox.Options)
	}
}

const testAccConfigFormFull = `
provider "fianu" {}

resource "fianu_form" "review" {
  path = "test.form.review"
  name = "Vendor Review"

  detail = {
    display_key = "vendor-review"
    description = "Annual vendor security review"

    elements = [
      {
        name        = "Reviewer name"
        type        = "text"
        required    = true
        description = "Full legal name"
        options = jsonencode({
          validation = true
          expression = "^[A-Za-z ]+$"
        })
      },
      {
        name = "Scope of review"
        type = "blob"
        options = jsonencode({
          placeholder = "Describe the scope"
        })
      },
      {
        name = "Residual risk"
        type = "radio"
        options = jsonencode({
          values = { high = "High", medium = "Medium", low = "Low" }
        })
      },
      {
        name = "Controls verified"
        type = "checkbox"
      },
    ]
  }
}
`

// TestAccFianuForm_Update proves an in-place change round-trips: the second
// deploy is not swallowed by the idempotency gate, and appending an element
// renumbers nothing before it.
func TestAccFianuForm_Update(t *testing.T) {
	stub := newFormStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigFormMinimal,
				Check:  resource.TestCheckResourceAttr("fianu_form.review", "detail.elements.#", "1"),
			},
			{
				Config: testAccConfigFormUpdated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_form.review", "detail.elements.#", "2"),
					resource.TestCheckResourceAttr("fianu_form.review", "detail.display_key", "change-review"),
				),
			},
		},
	})

	got := stub.captured(t)
	if len(got.Detail.Elements) != 2 {
		t.Fatalf("after update, elements = %d, want 2", len(got.Detail.Elements))
	}
	if got.Detail.Elements[0].Name != "Did you review the change?" {
		t.Errorf("after update, elements[0].name = %q — appending must not disturb earlier elements", got.Detail.Elements[0].Name)
	}
	if got.Detail.Elements[1].Code != 1 {
		t.Errorf("after update, elements[1].code = %d, want 1", got.Detail.Elements[1].Code)
	}
	if stub.deployHits.Load() < 2 {
		t.Errorf("deploy hits = %d, want at least 2 — the update never reached the server", stub.deployHits.Load())
	}
}

const testAccConfigFormUpdated = `
provider "fianu" {}

resource "fianu_form" "review" {
  path = "test.form.review"
  name = "Change Review"

  detail = {
    display_key = "change-review"

    elements = [
      {
        name = "Did you review the change?"
        type = "boolean"
        options = jsonencode({
          values = { yes = "Yes", no = "No" }
        })
      },
      {
        name     = "Reviewer notes"
        type     = "blob"
        required = true
      },
    ]
  }
}
`

// TestAccFianuForm_Import proves the composite ID round-trips through
// `terraform import`.
func TestAccFianuForm_Import(t *testing.T) {
	stub := newFormStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigFormMinimal},
			{
				Config:            testAccConfigFormMinimal,
				ResourceName:      "fianu_form.review",
				ImportState:       true,
				ImportStateId:     "form/test.form.review",
				ImportStateVerify: true,
				// detail stays user-authored — Read hydrates the envelope only,
				// because the server sorts elements, stamps fresh element UUIDs
				// and prunes `options` to the keys the type recognises.
				ImportStateVerifyIgnore: []string{"detail"},
			},
		},
	})
}

// TestAccFianuForm_RejectsMalformedOptions pins the plan-time guard. A bad
// options string would otherwise be dropped silently in buildEntity, deploying
// a question that accepts anything.
func TestAccFianuForm_RejectsMalformedOptions(t *testing.T) {
	stub := newFormStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigFormBadOptions,
				ExpectError: regexp.MustCompile(`value is not a JSON object`),
			},
		},
	})
}

const testAccConfigFormBadOptions = `
provider "fianu" {}

resource "fianu_form" "review" {
  path = "test.form.review"
  name = "Change Review"

  detail = {
    elements = [
      {
        name    = "Did you review the change?"
        type    = "boolean"
        options = "{not json"
      },
    ]
  }
}
`

// TestAccFianuForm_RejectsUnsupportedElementType guards the narrowed enum.
// `dropdown` is in the SQL enum but GetAsFormElementImpl cannot build it, so
// without the plan-time check it fails deep inside InsertForm with no attribute
// path to point at.
func TestAccFianuForm_RejectsUnsupportedElementType(t *testing.T) {
	stub := newFormStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigFormDropdown,
				ExpectError: regexp.MustCompile(`Attribute detail\.elements\[0\]\.type value must be one of`),
			},
		},
	})
}

const testAccConfigFormDropdown = `
provider "fianu" {}

resource "fianu_form" "review" {
  path = "test.form.review"
  name = "Change Review"

  detail = {
    elements = [
      {
        name = "Pick one"
        type = "dropdown"
      },
    ]
  }
}
`

// TestAccFianuForm_RejectsEmptyElements pins the list-size validator.
// Form.Validate returns "form must have at least one element", but only after
// the round trip; catching it at plan time keeps the error on the right line.
func TestAccFianuForm_RejectsEmptyElements(t *testing.T) {
	stub := newFormStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigFormNoElements,
				ExpectError: regexp.MustCompile(`list must contain at least 1 element`),
			},
		},
	})
}

const testAccConfigFormNoElements = `
provider "fianu" {}

resource "fianu_form" "review" {
  path = "test.form.review"
  name = "Change Review"

  detail = {
    elements = []
  }
}
`

func setEnv(t *testing.T, stub *formStub) {
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

// formStub fakes Console for the form resource: deploy captures the multipart
// entity, GET echoes it back so Read doesn't drift, DELETE counts archives.
type formStub struct {
	server       *httptest.Server
	deployHits   atomic.Int32
	archiveHits  atomic.Int32
	archivedPath atomic.Value // string
	stored       atomic.Value // *transportv1.DeployEntityFileResponse
	capturedVal  atomic.Value // *fianu_entities.Form
	lastCaptured atomic.Value // *fianu_entities.Form, never cleared
}

func (s *formStub) captured(t *testing.T) *fianu_entities.Form {
	t.Helper()
	e, _ := s.lastCaptured.Load().(*fianu_entities.Form)
	if e == nil {
		t.Fatal("no form captured on the deploy route")
	}
	return e
}

func newFormStub(t *testing.T) *formStub {
	t.Helper()
	stub := &formStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/entities/artifacts/deploy", func(w http.ResponseWriter, r *http.Request) {
		stub.deployHits.Add(1)
		req, raw := decodeDeployRequest(r)

		var e fianu_entities.Form
		if err := json.Unmarshal(raw, &e); err == nil {
			stub.capturedVal.Store(&e)
			stub.lastCaptured.Store(&e)
		}

		// Reject anything the real endpoint would: the deploy allowlist is
		// keyed on General.entityType, so a resource sending the wrong one
		// would 400 in production but pass a stub that ignores it.
		if req.General.EntityType == nil || *req.General.EntityType != string(db_vars.EntityTypeForm) {
			http.Error(w, `{"error":"unsupported entity type"}`, http.StatusBadRequest)
			return
		}

		// Mirror the server's content-hash idempotency so a repeat apply
		// reports "skipped" instead of minting a version.
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
				EntityID:    "test-form-uuid",
				Path:        entityPath,
				Name:        e.Name,
				Version:     "1",
				EntityType:  string(db_vars.EntityTypeForm),
			},
		}
		stub.stored.Store(resp)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// The real read filters to active unless ?status= says otherwise. Mirroring
	// that is the point: a resource that deployed a draft form would 404 here,
	// exactly as it would in production.
	mux.HandleFunc("/api/entities/forms/", func(w http.ResponseWriter, r *http.Request) {
		captured, _ := stub.capturedVal.Load().(*fianu_entities.Form)
		if captured == nil {
			http.NotFound(w, r)
			return
		}
		wantStatus := r.URL.Query().Get("status")
		if wantStatus == "" {
			wantStatus = string(db_vars.EntityStatusActive)
		}
		if string(captured.Version.Status) != wantStatus {
			http.NotFound(w, r)
			return
		}
		out := *captured
		out.UUID = "test-form-uuid"
		out.Type = db_vars.EntityTypeForm
		out.Version.Semantic = "1"
		out.Version.UUID = "version-uuid"
		out.Version.Status = db_vars.EntityStatusActive
		out.Version.State = db_vars.EntityStatePublished
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
		stub.capturedVal.Store((*fianu_entities.Form)(nil))
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
