// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package control_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	pkgvariables "github.com/fianulabs/core/v2/external/pkg/variables"
	transportv1 "github.com/fianulabs/core/v2/external/transport/http/v1"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/fianulabs/terraform-provider-fianu/internal/provider"
)

// TestAccFianuControl_CreateReadDestroy is the headline acceptance test for
// fianu_control. It exercises:
//
//   - Create  → Deploy hits the stub Console with the expected payload
//   - Read    → FetchControl populates state with server-side fields
//   - Plan    → re-running terraform plan after apply yields zero diff
//     (proves the idempotency contract end-to-end through the
//     terraform-plugin-framework)
//   - Destroy → ArchiveEntity is called with the resource's UUID
//
// The test stands up a single httptest server impersonating Console; the
// stub serves /entities/artifacts/deploy, /controls/<key>, and the archive
// PUT path. Each handler records call counts for post-test assertions.
func TestAccFianuControl_CreateReadDestroy(t *testing.T) {
	stub := newConsoleStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	// Trailing slash means "use the empty base path" — disables the provider's
	// default /api prefix so the stub doesn't need to mount routes under /api.
	t.Setenv("FIANU_HOST", stub.server.URL+"/")
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigBasicControl,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_control.example", "path", "test.control.basic"),
					resource.TestCheckResourceAttr("fianu_control.example", "name", "Basic Test Control"),
					resource.TestCheckResourceAttr("fianu_control.example", "detail.full_name", "Basic Test Control"),
					resource.TestCheckResourceAttr("fianu_control.example", "detail.display_key", "BTC"),
					resource.TestCheckResourceAttrSet("fianu_control.example", "uuid"),
					resource.TestCheckResourceAttr("fianu_control.example", "id", "control/test.control.basic"),
				),
			},
			{
				Config: testAccConfigBasicControl,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	if stub.deployHits.Load() < 1 {
		t.Fatalf("expected /entities/artifacts/deploy to be called, got %d", stub.deployHits.Load())
	}
}

const testAccConfigBasicControl = `
provider "fianu" {}

resource "fianu_control" "example" {
  path = "test.control.basic"
  name = "Basic Test Control"
  detail = {
    full_name   = "Basic Test Control"
    display_key = "BTC"
    description = "Acceptance-test fixture"
  }
}
`

// protoV6Factories returns the protocol-v6 factories required by
// terraform-plugin-testing. The provider is constructed fresh per test run.
func protoV6Factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"fianu": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

// consoleStub is a single httptest server that fakes the Console endpoints
// the provider exercises. Routes:
//
//   - POST /entities/artifacts/deploy → returns action="created" then
//     "skipped" for repeats (mirrors the real idempotency gate)
//   - GET  /controls/{key}            → returns a Control with the same
//     identity the deploy stored
//   - DELETE /archive/<type>/<uuid>   → archive
//
// The stub records call counts on each path AND captures the most recent
// deployed entity (decoded from X-Fianu-Raw-Content) so tests can assert on
// the wire payload directly.
type consoleStub struct {
	server      *httptest.Server
	deployHits  atomic.Int32
	fetchHits   atomic.Int32
	archiveHits atomic.Int32
	testHits    atomic.Int32

	// stored remembers the most recently deployed response so subsequent reads
	// reflect the just-applied state.
	stored atomic.Value // *transportv1.DeployEntityFileResponse

	// capturedEntity is the most recent *fianu_entities.Control decoded from
	// the X-Fianu-Raw-Content header. Tests inspect this to verify the HCL
	// translated into the expected entity payload.
	capturedEntity atomic.Value // *fianu_entities.Control
	// capturedRawContent is the raw bytes that were base64-decoded from the
	// header. Useful for asserting evaluation[].content round-trips byte-for-byte.
	capturedRawContent atomic.Value // []byte

	// capturedTestEntity is the most recent *fianu_entities.Control decoded
	// from the X-Fianu-Raw-Content header on a /entities/artifacts/test call.
	// Lets action-trigger tests assert which entity the action ran against.
	capturedTestEntity atomic.Value // *fianu_entities.Control
}

func newConsoleStub(t *testing.T) *consoleStub {
	t.Helper()
	stub := &consoleStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/entities/artifacts/deploy", func(w http.ResponseWriter, r *http.Request) {
		stub.deployHits.Add(1)

		// Pull the General envelope (for path echo-back), raw entity JSON
		// (for byte-for-byte assertions), and decoded Control (for shape
		// assertions) out of the multipart form in one pass.
		req, rawBytes, entity := decodeMultipartEntity(r)
		path := ""
		if req.General.Path != nil {
			path = *req.General.Path
		}
		entityName := ""
		if rawBytes != nil {
			stub.capturedRawContent.Store(rawBytes)
		}
		if entity != nil {
			stub.capturedEntity.Store(entity)
			entityName = entity.Name
		}

		// Second deploy with same content is a no-op per the real gate; the
		// stub mimics that by inspecting the system-hash header against the
		// prior call.
		action := "created"
		if prior := stub.stored.Load(); prior != nil {
			pr := prior.(*transportv1.DeployEntityFileResponse)
			if pr.Metadata != nil && pr.Metadata.ContentHash == r.Header.Get(pkgvariables.XFianuCISystemHash) {
				action = "skipped"
			} else {
				action = "updated"
			}
		}

		// Echo the deployed entity's name back so the provider's hydrate path
		// doesn't overwrite the user-authored value with a stub-side default.
		respName := entityName
		if respName == "" {
			respName = "Basic Test Control"
		}
		resp := &transportv1.DeployEntityFileResponse{
			Message: "ok",
			Metadata: &transportv1.DeploymentMetadata{
				Action:      action,
				ContentHash: r.Header.Get(pkgvariables.XFianuCISystemHash),
				EntityID:    "test-uuid-fixed",
				Path:        path,
				Name:        respName,
				Version:     "1",
				EntityType:  "control",
			},
		}
		stub.stored.Store(resp)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/entities/controls/", func(w http.ResponseWriter, r *http.Request) {
		stub.fetchHits.Add(1)
		key := strings.TrimPrefix(r.URL.Path, "/api/entities/controls/")
		w.Header().Set("Content-Type", "application/json")

		// Echo back the most recently deployed entity so Read doesn't drift
		// against the user's HCL. Falls back to a vanilla fixture for tests
		// that fetch before any deploy lands.
		if captured, _ := stub.capturedEntity.Load().(*fianu_entities.Control); captured != nil {
			out := *captured // shallow copy is fine — we only mutate top-level
			out.UUID = "test-uuid-fixed"
			out.Type = "control"
			out.Version.Semantic = "1"
			out.Version.UUID = "version-uuid"
			out.Version.Status = "active"
			out.Version.State = "published"
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		_, _ = fmt.Fprintf(w, `{
  "uuid":"test-uuid-fixed",
  "name":"Basic Test Control",
  "path":%q,
  "type":"control",
  "version":{"semantic":"1","uuid":"version-uuid","status":"active","state":"published"},
  "detail":{"control":{"fullName":"Basic Test Control","displayKey":"BTC","description":"Acceptance-test fixture"}}
}`, key)
	})

	// /entities/artifacts/test is what fianu_control_test.Invoke hits. The
	// real server unpacks the entity, runs its rego rules against bundled
	// input/data fixtures, and returns a JUnit-shaped report. The stub
	// captures the entity and returns a single passing case so action_triggers
	// tests can assert the action ran without tripping the action's failure
	// diagnostic.
	mux.HandleFunc("/api/entities/artifacts/test", func(w http.ResponseWriter, r *http.Request) {
		stub.testHits.Add(1)

		var path, name string
		if _, _, entity := decodeMultipartEntity(r); entity != nil {
			stub.capturedTestEntity.Store(entity)
			path = entity.Path
			name = entity.Name
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(transportv1.TestEntityFileResponse{
			Path: path,
			Name: name,
			Report: map[string]any{
				"testsuites": []map[string]any{
					{
						"name": "rule_test.rego",
						"testcase": []map[string]any{
							{"name": "occ_case_1", "classname": "rule"},
						},
					},
				},
			},
		})
	})

	// ArchiveControl hits DELETE /api/entities/archive/control/<uuid>. Match
	// anything under that prefix to keep the stub forgiving.
	mux.HandleFunc("/api/entities/archive/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			stub.archiveHits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"archived"}`))
			return
		}
		http.NotFound(w, r)
	})

	stub.server = httptest.NewServer(mux)
	return stub
}

// decodeMultipartEntity pulls everything the stub needs from a deploy/test
// request in one pass. The provider sends multipart/form-data with two parts:
//
//   - `payload` form field — JSON-marshalled DeployEntityFileRequest (carries
//     the General envelope with entity_type / path / version).
//   - `file` file part (filename `entity.json`) — JSON-marshalled
//     *fianu_entities.Control, i.e. the same bytes the SDK used to send via
//     the X-Fianu-Raw-Content header before the multipart switch.
//
// Returns the parsed envelope, the raw entity bytes, and the decoded
// Control. Any may be zero/nil if the request is malformed or carries only
// some of the parts; the stub treats those as silent no-ops because the
// tests that depend on captured state assert non-nil directly.
func decodeMultipartEntity(r *http.Request) (transportv1.DeployEntityFileRequest, []byte, *fianu_entities.Control) {
	var req transportv1.DeployEntityFileRequest
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return req, nil, nil
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return req, nil, nil
	}
	if vals, ok := r.MultipartForm.Value["payload"]; ok && len(vals) > 0 {
		_ = json.Unmarshal([]byte(vals[0]), &req)
	}

	fileHeaders, ok := r.MultipartForm.File["file"]
	if !ok || len(fileHeaders) == 0 {
		return req, nil, nil
	}
	fh, err := fileHeaders[0].Open()
	if err != nil {
		return req, nil, nil
	}
	defer fh.Close()
	raw, err := io.ReadAll(fh)
	if err != nil {
		return req, nil, nil
	}

	var entity fianu_entities.Control
	if err := json.Unmarshal(raw, &entity); err != nil {
		return req, raw, nil
	}
	return req, raw, &entity
}

// TestAccFianuControl_FullSpec exercises the full HCL schema introduced in
// Phase 1.1: documentation, results, relations, assets, policy_template
// (with nested measures), evaluation cases (rule + python content), and
// config. The test asserts the stub server received an entity with each
// section populated — proving the HCL → Go entity translation is faithful.
func TestAccFianuControl_FullSpec(t *testing.T) {
	stub := newConsoleStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigFullSpec},
			// Re-plan: identical content must produce zero diff.
			{
				Config: testAccConfigFullSpec,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	captured, _ := stub.capturedEntity.Load().(*fianu_entities.Control)
	if captured == nil {
		t.Fatalf("expected the stub to have captured a deployed entity, got nil")
	}

	// Identity round-trip
	if captured.Path != "test.full.spec.checkmarx" {
		t.Errorf("entity Path = %q, want %q", captured.Path, "test.full.spec.checkmarx")
	}
	if captured.Detail.Control == nil || captured.Detail.Control.DisplayKey != "FSPEC" {
		t.Errorf("ControlInfo.DisplayKey not propagated: %+v", captured.Detail.Control)
	}

	// Documentation
	if len(captured.Detail.Documentation) != 1 || captured.Detail.Documentation[0].Title != "Vendor docs" {
		t.Errorf("Documentation not populated: %+v", captured.Detail.Documentation)
	}

	// Results: fail=true should be on the wire as the bool true.
	if v, ok := captured.Detail.Results["fail"]; !ok || v != true {
		t.Errorf("Results[fail] = %v ok=%v, want true", v, ok)
	}

	// Relations
	if len(captured.Detail.Relations) != 1 {
		t.Fatalf("expected 1 relation, got %d", len(captured.Detail.Relations))
	}
	rel := captured.Detail.Relations[0]
	if rel.Domain != "compliance.controls" || rel.Collection != "security" {
		t.Errorf("relation domain/collection wrong: %+v", rel)
	}
	if rel.Producer == nil || rel.Producer.Path != "checkmarx" {
		t.Errorf("relation producer not propagated: %+v", rel.Producer)
	}

	// Assets
	if len(captured.Detail.Assets) != 1 || string(captured.Detail.Assets[0].Type) != "module" {
		t.Errorf("Assets not propagated: %+v", captured.Detail.Assets)
	}

	// Policy template measures (nested 3 levels: vulnerabilities → critical → maximum)
	measures := captured.Detail.PolicyTemplate.Measures
	if len(measures) == 0 {
		t.Fatal("PolicyTemplate.Measures empty")
	}
	var sawMaximum bool
	for _, m := range measures {
		if m.Name != "vulnerabilities" {
			continue
		}
		for _, child := range m.Children {
			if child.Name != "critical" {
				continue
			}
			for _, leaf := range child.Children {
				if leaf.Name == "maximum" && leaf.Type == "metric" {
					sawMaximum = true
				}
			}
		}
	}
	if !sawMaximum {
		t.Errorf("expected vulnerabilities.critical.maximum measure leaf in tree: %+v", measures)
	}

	// Evaluation cases — each entry's Detail (raw bytes) must equal the content
	// the HCL passed in.
	if len(captured.Detail.Evaluation) < 2 {
		t.Fatalf("expected ≥2 evaluation cases, got %d", len(captured.Detail.Evaluation))
	}
	var ruleCase *fianu_entities.Case
	for i := range captured.Detail.Evaluation {
		if string(captured.Detail.Evaluation[i].Type) == "rule" {
			ruleCase = &captured.Detail.Evaluation[i]
			break
		}
	}
	if ruleCase == nil {
		t.Fatal("expected a 'rule' evaluation case, found none")
	}
	if !strings.Contains(string(ruleCase.Detail), "package rule") {
		t.Errorf("rule case content didn't round-trip: got %q", string(ruleCase.Detail))
	}
}

const testAccConfigFullSpec = `
provider "fianu" {}

resource "fianu_control" "full" {
  path = "test.full.spec.checkmarx"
  name = "Full Spec Test Control"
  detail = {
    full_name   = "Full Spec Acceptance Control"
    display_key = "FSPEC"
    description = "Exercises every detail section."

    documentation = [
      { title = "Vendor docs", url = "https://example.com/docs" },
    ]

    results = { fail = true }

    relations = [{
      domain     = "compliance.controls"
      collection = "security"
      path       = "checkmarx.sast"
      note       = "occurrence"
      producer   = { type = "plugin", path = "checkmarx" }
    }]

    assets = [{
      type = "module"
      series = [
        { name = "commit" },
      ]
    }]

    policy_template = {
      measures = [{
        name = "vulnerabilities"
        type = "section"
        children = [{
          name = "critical"
          type = "section"
          children = [{
            name = "maximum"
            type = "metric"
            value = "number"
          }]
        }]
      }]
    }

    evaluation = [
      { type = "rule", engine = "opa", label = "rule.rego", content = "package rule\ndefault pass = false\n" },
      { type = "detail", label = "detail.py", content = "def main(occurrence, context):\n  return {'ok': True}\n" },
    ]

    config = {
      scope = "commit"
    }
  }
}
`

// TestAccFianuControl_EvaluationContent_RoundTrips asserts that an
// evaluation case's content arrives at the server byte-for-byte. This is the
// load-bearing contract for `file()`-loaded rule.rego / detail.py.
func TestAccFianuControl_EvaluationContent_RoundTrips(t *testing.T) {
	stub := newConsoleStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	wanted := "package rule\nimport future.keywords\n\npass if { input.ok }\n"
	cfg := fmt.Sprintf(`
provider "fianu" {}
resource "fianu_control" "rt" {
  path = "test.eval.roundtrip"
  name = "Eval Round-trip"
  detail = {
    full_name   = "Eval Round-trip"
    display_key = "ERT"
    evaluation  = [{ type = "rule", engine = "opa", content = %q }]
  }
}
`, wanted)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps:                    []resource.TestStep{{Config: cfg}},
	})

	captured, _ := stub.capturedEntity.Load().(*fianu_entities.Control)
	if captured == nil {
		t.Fatal("no entity captured")
	}
	if len(captured.Detail.Evaluation) != 1 {
		t.Fatalf("expected 1 evaluation case, got %d", len(captured.Detail.Evaluation))
	}
	got := string(captured.Detail.Evaluation[0].Detail)
	if got != wanted {
		t.Errorf("evaluation content drifted on the wire:\nwant %q\ngot  %q", wanted, got)
	}
}

// TestAccFianuControl_ActionTriggers proves the end-to-end wiring of the
// fianu_control_test action when invoked via lifecycle.action_trigger.
//
// HCL surface under test:
//
//   - resource "fianu_control" with `lifecycle.action_trigger { events =
//     [after_create]; actions = [action.fianu_control_test.t] }`
//   - action "fianu_control_test" "t" with the matching evaluation cases
//
// On apply, terraform CLI parses the action block, invokes the action after
// the resource is created, and the action calls /entities/artifacts/test.
// The stub records that hit; the assertion proves the action fired at least
// once, which is the load-bearing claim of the auto-test feature.
//
// Requires terraform CLI ≥ 1.14 (the CLI version that introduced action
// blocks and lifecycle.action_trigger). Older binaries fail the HCL parse.
func TestAccFianuControl_ActionTriggers(t *testing.T) {
	stub := newConsoleStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigActionTriggers},
		},
	})

	if hits := stub.testHits.Load(); hits < 1 {
		t.Fatalf("expected /entities/artifacts/test to be called via action_trigger, got %d", hits)
	}

	captured, _ := stub.capturedTestEntity.Load().(*fianu_entities.Control)
	if captured == nil {
		t.Fatal("action invoked /entities/artifacts/test but no entity was captured by the stub")
	}
	if captured.Path != "test.action.triggers" {
		t.Errorf("test entity Path = %q, want %q", captured.Path, "test.action.triggers")
	}
	if len(captured.Detail.Evaluation) == 0 {
		t.Errorf("test entity must carry evaluation cases for the rego runner; got 0")
	}
}

const testAccConfigActionTriggers = `
provider "fianu" {}

locals {
  evaluation = [
    { type = "rule", engine = "opa", label = "rule.rego", content = "package rule\ndefault pass = false\n" },
    { type = "input", label = "occ_case_1.json", content = "{\"ok\":true}" },
  ]
}

resource "fianu_control" "trig" {
  path = "test.action.triggers"
  name = "Action Trigger Test Control"
  detail = {
    full_name   = "Action Trigger Test"
    display_key = "ATRIG"
    evaluation  = local.evaluation
  }

  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.fianu_control_test.t]
    }
  }
}

action "fianu_control_test" "t" {
  config {
    path       = "test.action.triggers"
    name       = "Action Trigger Test Control"
    evaluation = local.evaluation
  }
}
`

// TestAccFianuControlTest_RejectsInvalidType proves the action's
// evaluation[].type validator catches typos at plan time. The OneOf
// validator should refuse a `type = "rulez"` entry before terraform attempts
// to apply, so users get fast feedback in their editor / CI plan step.
func TestAccFianuControlTest_RejectsInvalidType(t *testing.T) {
	stub := newConsoleStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigInvalidEvaluationType,
				ExpectError: regexpInvalidType,
			},
		},
	})
}

// regexpInvalidType anchors specifically to terraform-plugin-framework's
// stringvalidator.OneOf failure message. Tighter than a generic "invalid
// attribute" pattern so the test catches only the bad-enum case it claims
// to test, not unrelated validation failures elsewhere in the HCL.
var regexpInvalidType = regexp.MustCompile(`(?i)value must be one of`)

const testAccConfigInvalidEvaluationType = `
provider "fianu" {}

action "fianu_control_test" "bad" {
  config {
    path = "x"
    name = "X"
    evaluation = [
      { type = "rulez", engine = "opa", content = "package rule" },
    ]
  }
}
`
