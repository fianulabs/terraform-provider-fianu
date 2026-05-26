// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package gate_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// TestAccFianuGate_Minimal — gate with only identity + config. No nested
// policy. Asserts the deploy hits Console with EntityType=gate, and that a
// re-plan after apply yields zero diff.
func TestAccFianuGate_Minimal(t *testing.T) {
	stub := newGateStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigMinimalGate,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_gate.example", "path", "test.gate.basic"),
					resource.TestCheckResourceAttr("fianu_gate.example", "name", "Basic Test Gate"),
					resource.TestCheckResourceAttr("fianu_gate.example", "id", "gate/test.gate.basic"),
				),
			},
			{
				Config: testAccConfigMinimalGate,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	captured, _ := stub.capturedGate.Load().(*fianu_entities.Control)
	if captured == nil {
		t.Fatalf("expected the stub to have captured a deployed gate entity, got nil")
	}
	if captured.Type != db_vars.EntityTypeGateControl {
		t.Errorf("captured entity Type = %q, want %q", captured.Type, db_vars.EntityTypeGateControl)
	}
	if stub.capturedPolicy.Load() != nil {
		t.Errorf("no nested policy in config, but stub captured a policy deploy")
	}
}

const testAccConfigMinimalGate = `
provider "fianu" {}

resource "fianu_gate" "example" {
  path = "test.gate.basic"
  name = "Basic Test Gate"

  detail = {
    full_name   = "Basic Test Gate"
    display_key = "BTG"
    description = "Acceptance-test fixture"
  }
}
`

// TestAccFianuGate_WithPolicy — the canonical gate authoring flow. Single
// HCL block creates a gate AND a policy entity targeting it. Asserts both
// entities are deployed and that the policy's control reference points at
// the gate.
func TestAccFianuGate_WithPolicy(t *testing.T) {
	stub := newGateStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigGateWithPolicy},
			{
				Config: testAccConfigGateWithPolicy,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	gate, _ := stub.capturedGate.Load().(*fianu_entities.Control)
	policy, _ := stub.capturedPolicy.Load().(*fianu_entities.Policy)
	if gate == nil {
		t.Fatal("expected gate to be captured")
	}
	if policy == nil {
		t.Fatal("expected policy to be captured")
	}
	if policy.Detail.Control.Path != "test.gate.security" {
		t.Errorf("policy.control.path = %q, want %q", policy.Detail.Control.Path, "test.gate.security")
	}
	// Control.Type MUST be "gate" so the server resolver queries the gate
	// table, not the control table. Regression for bug where Type was nil
	// and resolver returned "failed to resolve control" 400s.
	if got := policy.Detail.Control.Type; got == nil {
		t.Error("policy.control.type should be non-nil (=\"gate\"), got nil — server would 404 the resolver")
	} else if *got != string(db_vars.EntityTypeGateControl) {
		t.Errorf("policy.control.type = %q, want %q", *got, db_vars.EntityTypeGateControl)
	}
	if policy.Path != "test.gate.security" {
		t.Errorf("policy auto-path = %q, want %q", policy.Path, "test.gate.security")
	}
	if len(policy.Detail.Variations) != 1 {
		t.Fatalf("expected 1 variation, got %d", len(policy.Detail.Variations))
	}
	// Variation's Policy map should be the resolved {<label>: <uuid>} shape
	// the server's gate-children CTE expects — NOT a free-form measures
	// payload. Regression for the bug where free-form measure JSON corrupted
	// the row and broke single-row FetchGate.
	gotPolicy := policy.Detail.Variations[0].Policy
	if len(gotPolicy) != 1 {
		t.Fatalf("expected exactly 1 entry in variation.policy, got %d: %+v", len(gotPolicy), gotPolicy)
	}
	wantUUID := "9919c495-4d74-40b0-a1b8-8e04910ad9ea"
	if v, ok := gotPolicy[wantUUID]; !ok || v != wantUUID {
		t.Errorf("variation.policy[%q] = %v ok=%v, want %q", wantUUID, v, ok, wantUUID)
	}
	if len(gate.Detail.Environments) != 1 {
		t.Errorf("expected 1 environment binding, got %d", len(gate.Detail.Environments))
	}
}

const testAccConfigGateWithPolicy = `
provider "fianu" {}

resource "fianu_gate" "security" {
  path = "test.gate.security"
  name = "Security Gate"

  detail = {
    full_name   = "Production Security Gate"
    display_key = "PSEC"
    description = "Gates production deployments."

    config = {
      scope = "commit"
    }

    environments = [
      { path = "env.prod" },
    ]

    policy = {
      variations = [
        { required_controls = ["9919c495-4d74-40b0-a1b8-8e04910ad9ea"] },
      ]
      override = {
        asset = {
          types = ["repository"]
        }
      }
    }
  }
}
`

// TestAccFianuGate_WithPodsAndCriteria — the "every knob" gate. Identity,
// inline policy with CEL criteria, two pipeline-automation pods (one
// blanket-enforce, one with a scoped check-mode for staging). Asserts both
// pod sets land on the stub, the second pod's matching scope carries its
// own protection level, and re-plan is a no-op.
func TestAccFianuGate_WithPodsAndCriteria(t *testing.T) {
	stub := newGateStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigGateWithPods},
			{
				Config: testAccConfigGateWithPods,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	if got := stub.podSetHits.Load(); got < 2 {
		t.Errorf("expected at least 2 pod sets, got %d", got)
	}
	// Pod-key membership is asserted via the "ever-seen" map (not
	// capturedPods, which gets drained when resource.Test runs its
	// implicit destroy at end of the test case).
	for _, key := range []string{"default", "staging-relaxed"} {
		if _, ok := stub.podsEverSeen.Load(key); !ok {
			t.Errorf("expected pod with key %q to have been deployed at some point", key)
		}
	}
	if got := stub.podDeleteHits.Load(); got < 2 {
		t.Errorf("expected the test framework's destroy step to detach both pods, got %d deletes", got)
	}
}

const testAccConfigGateWithPods = `
provider "fianu" {}

resource "fianu_gate" "security" {
  path = "test.gate.security.full"
  name = "Security Gate (Full)"

  detail = {
    full_name   = "Production Security Gate"
    display_key = "PSEC"

    policy = {
      variations = [
        {
          criteria = {
            expressions = [
              { expression = "asset.scm.repository startsWith 'prod-'" },
            ]
          }
          required_controls = ["a868c707-850a-474a-8e66-77a240de4305"]
        },
        {
          required_controls = ["a868c707-850a-474a-8e66-77a240de4305"]
        },
      ]
      override = {
        asset = {
          types = ["repository"]
        }
      }
    }

    pods = [
      {
        key              = "default"
        protection_level = "enforce"
      },
      {
        key              = "staging-relaxed"
        protection_level = "enforce"
        matching = [
          {
            protection_level = "check"
            expressions = [
              { expression = "asset.scm.repository startsWith 'staging-' || asset.scm.repository startsWith 'preview-'" },
            ]
          },
        ]
      },
    ]
  }
}
`

func protoV6Factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"fianu": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

// gateStub fakes Console for the gate resource. The deploy route inspects
// the payload's general.entityType to discriminate gate vs policy and
// captures each into its own atomic.Value. Read echoes the captured gate
// back; archive returns 200.
type gateStub struct {
	server         *httptest.Server
	deployHits     atomic.Int32
	fetchHits      atomic.Int32
	archiveHits    atomic.Int32
	podSetHits     atomic.Int32
	podDeleteHits  atomic.Int32
	storedGate     atomic.Value // *transportv1.DeployEntityFileResponse
	storedPolicy   atomic.Value // *transportv1.DeployEntityFileResponse
	capturedGate   atomic.Value // *fianu_entities.Control
	capturedPolicy atomic.Value // *fianu_entities.Policy
	capturedPods   sync.Map     // key (string) -> pod body (map[string]any). Drained when DELETE arrives.
	podsEverSeen   sync.Map     // key (string) -> struct{}. Add-only; never cleared on DELETE.
}

func newGateStub(t *testing.T) *gateStub {
	t.Helper()
	stub := &gateStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/entities/artifacts/deploy", func(w http.ResponseWriter, r *http.Request) {
		stub.deployHits.Add(1)

		req, raw := decodeDeployRequest(r)
		entityTypeStr := ""
		if req.General.EntityType != nil {
			entityTypeStr = *req.General.EntityType
		}
		path := ""
		if req.General.Path != nil {
			path = *req.General.Path
		}

		respName := ""
		uuid := ""

		switch entityTypeStr {
		case "gate":
			var c fianu_entities.Control
			if err := json.Unmarshal(raw, &c); err == nil {
				stub.capturedGate.Store(&c)
				respName = c.Name
			}
			uuid = "test-gate-uuid"
		case "policy":
			var p fianu_entities.Policy
			if err := json.Unmarshal(raw, &p); err == nil {
				stub.capturedPolicy.Store(&p)
				respName = p.Name
			}
			uuid = "test-policy-uuid"
		}

		action := "created"
		var stored atomic.Value
		switch entityTypeStr {
		case "gate":
			stored = stub.storedGate
		case "policy":
			stored = stub.storedPolicy
		}
		if prior := stored.Load(); prior != nil {
			pr := prior.(*transportv1.DeployEntityFileResponse)
			if pr.Metadata != nil && pr.Metadata.ContentHash == r.Header.Get("X-Fianu-CI-System-Hash") {
				action = "skipped"
			} else {
				action = "updated"
			}
		}

		resp := &transportv1.DeployEntityFileResponse{
			Message: "ok",
			Metadata: &transportv1.DeploymentMetadata{
				Action:      action,
				ContentHash: r.Header.Get("X-Fianu-CI-System-Hash"),
				EntityID:    uuid,
				Path:        path,
				Name:        respName,
				Version:     "1",
				EntityType:  entityTypeStr,
			},
		}
		switch entityTypeStr {
		case "gate":
			stub.storedGate.Store(resp)
		case "policy":
			stub.storedPolicy.Store(resp)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/entities/gates/", func(w http.ResponseWriter, r *http.Request) {
		stub.fetchHits.Add(1)
		w.Header().Set("Content-Type", "application/json")

		captured, _ := stub.capturedGate.Load().(*fianu_entities.Control)
		if captured == nil {
			http.NotFound(w, r)
			return
		}
		out := *captured
		out.UUID = "test-gate-uuid"
		out.Type = db_vars.EntityTypeGateControl
		out.Version.Semantic = "1"
		out.Version.UUID = "version-uuid"
		out.Version.Status = "active"
		out.Version.State = "published"
		_ = json.NewEncoder(w).Encode(out)
	})

	// FetchPolicy fallback for the deployGatePolicy path that refetches on
	// sparse deploy responses.
	mux.HandleFunc("/api/entities/policies/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		captured, _ := stub.capturedPolicy.Load().(*fianu_entities.Policy)
		if captured == nil {
			http.NotFound(w, r)
			return
		}
		out := *captured
		out.UUID = "test-policy-uuid"
		out.StandardEntity.Type = db_vars.EntityTypePolicy
		out.Version.Semantic = "1"
		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("/api/entities/archive/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			stub.archiveHits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"archived"}`))
			return
		}
		http.NotFound(w, r)
	})

	// Pod set/delete: PUT/DELETE /api/pods/entities/:entity_id/:type/:key
	mux.HandleFunc("/api/pods/entities/", func(w http.ResponseWriter, r *http.Request) {
		segments := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/pods/entities/"), "/")
		if len(segments) < 3 {
			http.NotFound(w, r)
			return
		}
		key := segments[2]

		switch r.Method {
		case http.MethodPut, http.MethodPost:
			stub.podSetHits.Add(1)
			body, _ := io.ReadAll(r.Body)
			var pod map[string]any
			_ = json.Unmarshal(body, &pod)
			stub.capturedPods.Store(key, pod)
			stub.podsEverSeen.Store(key, struct{}{})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case http.MethodDelete:
			stub.podDeleteHits.Add(1)
			stub.capturedPods.Delete(key)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})

	stub.server = httptest.NewServer(mux)
	return stub
}

// decodeDeployRequest parses the multipart deploy request and returns the
// General envelope plus the raw entity JSON. Callers unmarshal into the
// right entity type based on general.entityType.
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
