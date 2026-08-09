// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package gate_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	v, ok := gotPolicy[wantUUID]
	if !ok {
		t.Fatalf("variation.policy missing key %q, got: %+v", wantUUID, gotPolicy)
	}
	if v != wantUUID {
		t.Errorf("variation.policy[%q] = %v, want %q", wantUUID, v, wantUUID)
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

// TestAccFianuGate_WithChecksAndCriteria — the "every knob" gate. Identity,
// inline policy with CEL criteria, two gate-native checks (one blanket-enforce,
// one with a scoped check-mode for staging). Asserts both checks land on the
// deployed entity at detail.gate.checks, the second check's matching scope
// carries its own protection level, and re-plan is a no-op.
func TestAccFianuGate_WithChecksAndCriteria(t *testing.T) {
	stub := newGateStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigGateWithChecks},
			{
				Config: testAccConfigGateWithChecks,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	gate, _ := stub.capturedGate.Load().(*fianu_entities.Control)
	if gate == nil {
		t.Fatal("expected gate captured")
	}
	cfg := gate.Detail.Gate
	if cfg == nil {
		t.Fatal("detail.gate is nil — gate-native check config never reached the wire")
	}
	if !cfg.IsEnabled() {
		t.Error("detail.gate.enabled should be true")
	}
	if len(cfg.Checks) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(cfg.Checks))
	}

	blanket := cfg.Checks[0]
	if blanket.Name != "default" {
		t.Errorf("checks[0].name = %q, want %q", blanket.Name, "default")
	}
	if blanket.ProtectionLevel != fianu_entities.ProtectionLevelEnforce {
		t.Errorf("checks[0].protectionLevel = %q, want enforce", blanket.ProtectionLevel)
	}
	if len(blanket.Matching) != 0 {
		t.Errorf("checks[0] should match unconditionally, got %d scopes", len(blanket.Matching))
	}
	if want := []string{"fianu"}; len(blanket.GatingSources) != 1 || blanket.GatingSources[0] != want[0] {
		t.Errorf("checks[0].gatingSources = %v, want %v", blanket.GatingSources, want)
	}
	if blanket.CompletionAction != fianu_entities.GateCompletionActionPostCheckStatus {
		t.Errorf("checks[0].completionAction = %q, want post_check_status", blanket.CompletionAction)
	}

	scoped := cfg.Checks[1]
	if scoped.Name != "staging-relaxed" {
		t.Errorf("checks[1].name = %q, want %q", scoped.Name, "staging-relaxed")
	}
	if len(scoped.Matching) != 1 {
		t.Fatalf("expected 1 matching scope on checks[1], got %d", len(scoped.Matching))
	}
	// The per-scope override is the whole point: the check defaults to
	// enforce, but staging/preview repos drop to check-only.
	if got := scoped.Matching[0].ProtectionLevel; got != fianu_entities.ProtectionLevelCheck {
		t.Errorf("checks[1].matching[0].protectionLevel = %q, want check", got)
	}
	if len(scoped.Matching[0].Expressions) != 1 {
		t.Fatalf("expected 1 expression on the scoped check, got %d", len(scoped.Matching[0].Expressions))
	}
}

const testAccConfigGateWithChecks = `
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
            asset = { type = "repository" }
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

    gate = {
      enabled = true
      checks = [
        {
          name              = "default"
          protection_level  = "enforce"
          gating_sources    = ["fianu"]
          completion_action = "post_check_status"
        },
        {
          name             = "staging-relaxed"
          protection_level = "enforce"
          matching = [
            {
              protection_level = "check"
              asset            = { type = "repository" }
              expressions = [
                { expression = "asset.scm.repository startsWith 'staging-' || asset.scm.repository startsWith 'preview-'" },
              ]
            },
          ]
        },
      ]
    }
  }
}
`

// TestAccFianuGate_CriteriaReferencesIndex verifies that gates can plumb the
// canonical "criteria.indexes" reference shape through to the wire — both on
// the inline policy's variation.criteria AND on gate.checks[].matching scopes.
// Without the symmetric expansion (asset + indexes alongside expressions),
// gates would force users to inline CEL even when a reusable fianu_index
// already exists.
func TestAccFianuGate_CriteriaReferencesIndex(t *testing.T) {
	stub := newGateStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigGateCriteriaIndexes},
			{
				Config: testAccConfigGateCriteriaIndexes,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	policy, _ := stub.capturedPolicy.Load().(*fianu_entities.Policy)
	if policy == nil {
		t.Fatal("expected policy captured")
	}
	if len(policy.Detail.Variations) != 1 {
		t.Fatalf("expected 1 variation, got %d", len(policy.Detail.Variations))
	}
	crit := policy.Detail.Variations[0].Criteria
	if crit == nil {
		t.Fatal("variation.criteria is nil")
	}
	if len(crit.Indexes) != 1 {
		t.Fatalf("expected 1 index ref on variation.criteria, got %d", len(crit.Indexes))
	}
	if got := crit.Indexes[0].IndexPath; got != "compliance.indexes.prod_repos" {
		t.Errorf("criteria.indexes[0].path = %q, want %q", got, "compliance.indexes.prod_repos")
	}
	if got := crit.Indexes[0].IndexID; got != "" {
		t.Errorf("criteria.indexes[0].id should be empty (path-form), got %q", got)
	}

	// checks[].matching also propagates indexes via the embedded
	// PolicyAssetGroup — a gate scoped by a reusable fianu_index instead of
	// inline CEL. Assert it landed on the deployed entity.
	gate, _ := stub.capturedGate.Load().(*fianu_entities.Control)
	if gate == nil || gate.Detail.Gate == nil {
		t.Fatal("expected gate with detail.gate captured")
	}
	if len(gate.Detail.Gate.Checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(gate.Detail.Gate.Checks))
	}
	matching := gate.Detail.Gate.Checks[0].Matching
	if len(matching) != 1 {
		t.Fatalf("check 'scoped-by-index' has no matching scopes: %+v", gate.Detail.Gate.Checks[0])
	}
	if len(matching[0].Indexes) != 1 {
		t.Fatalf("check matching scope missing indexes ref: %+v", matching[0])
	}
	if got := matching[0].Indexes[0].IndexPath; got != "compliance.indexes.staging_repos" {
		t.Errorf("check matching scope indexes[0].path = %q, want compliance.indexes.staging_repos", got)
	}
}

const testAccConfigGateCriteriaIndexes = `
provider "fianu" {}

resource "fianu_gate" "security" {
  path = "test.gate.security.indexed"
  name = "Security Gate (Indexed)"

  detail = {
    full_name   = "Production Security Gate"
    display_key = "PSEC2"

    policy = {
      variations = [
        {
          criteria = {
            indexes = [
              { path = "compliance.indexes.prod_repos" },
            ]
          }
          required_controls = ["a868c707-850a-474a-8e66-77a240de4305"]
        },
      ]
      override = {
        asset = {
          types = ["repository"]
        }
      }
    }

    gate = {
      enabled = true
      checks = [
        {
          name             = "scoped-by-index"
          protection_level = "enforce"
          matching = [
            {
              protection_level = "check"
              indexes = [
                { path = "compliance.indexes.staging_repos" },
              ]
            },
          ]
        },
      ]
    }
  }
}
`

// TestAccFianuGate_OverrideLandsOnAssets pins the override -> Detail.Assets
// mapping. The provider stopped writing the deprecated Detail.Override and
// relies on the server deriving it from Detail.Assets
// (buildOverrideFromAssets, core/pkg/policies/service.go). That only resolves
// identically if each ref lands in the arm it used to: a bare Path goes to
// Types, and an "asset"-tagged ref goes to Explicit verbatim — including for
// entity keys that are not UUIDs.
func TestAccFianuGate_OverrideLandsOnAssets(t *testing.T) {
	stub := newGateStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigGateOverride},
			{
				Config: testAccConfigGateOverride,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	policy, _ := stub.capturedPolicy.Load().(*fianu_entities.Policy)
	if policy == nil {
		t.Fatal("expected policy captured")
	}
	// The deprecated field must stay unset — that is the whole point.
	if policy.Detail.Override != nil { //nolint:staticcheck // asserting we do NOT write the deprecated field
		t.Errorf("Detail.Override should not be written, got %+v", policy.Detail.Override) //nolint:staticcheck
	}
	if len(policy.Detail.Assets) != 3 {
		t.Fatalf("expected 3 asset refs (2 types + 1 explicit), got %d: %+v", len(policy.Detail.Assets), policy.Detail.Assets)
	}

	// Types: Path set, no UUID and no AssetType, so buildOverrideFromAssets
	// falls to its default arm and appends to Types.
	for i, want := range []string{"repository", "module"} {
		got := policy.Detail.Assets[i]
		if got.Path != want {
			t.Errorf("assets[%d].Path = %q, want %q", i, got.Path, want)
		}
		if got.UUID != "" || got.AssetType != "" {
			t.Errorf("assets[%d] should carry only Path, got %+v", i, got)
		}
	}

	// Explicit: tagged so it routes to Explicit regardless of whether the
	// value parses as a UUID.
	if got := policy.Detail.Assets[2]; got.UUID != "some.explicit.asset" || got.AssetType != "asset" {
		t.Errorf("explicit ref = %+v, want UUID=some.explicit.asset AssetType=asset", got)
	}
}

const testAccConfigGateOverride = `
provider "fianu" {}

resource "fianu_gate" "security" {
  path = "test.gate.security.override"
  name = "Security Gate (Override)"

  detail = {
    full_name   = "Production Security Gate"
    display_key = "PSEC3"

    policy = {
      variations = [
        {
          required_controls = ["a868c707-850a-474a-8e66-77a240de4305"]
        },
      ]
      override = {
        asset = {
          types    = ["repository", "module"]
          explicit = ["some.explicit.asset"]
        }
      }
    }
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
	storedGate     atomic.Value // *transportv1.DeployEntityFileResponse
	storedPolicy   atomic.Value // *transportv1.DeployEntityFileResponse
	capturedGate   atomic.Value // *fianu_entities.Control
	capturedPolicy atomic.Value // *fianu_entities.Policy
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

// TestAccFianuGate_Import proves the composite ID round-trips through
// `terraform import`. gateModel.Detail is a value type, so ImportState has to
// seed it before the post-import Read runs — this pins the behaviour that
// collection, environment and target were all missing.
func TestAccFianuGate_Import(t *testing.T) {
	stub := newGateStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigMinimalGate},
			{
				Config:            testAccConfigMinimalGate,
				ResourceName:      "fianu_gate.example",
				ImportState:       true,
				ImportStateId:     "gate/test.gate.basic",
				ImportStateVerify: true,
				// detail stays user-authored — Read hydrates the envelope plus
				// the ControlInfo trio only.
				ImportStateVerifyIgnore: []string{"detail"},
			},
		},
	})
}

// TestAccFianuGate_Update covers the second-apply path on the most involved
// applyPlan in the provider: a gate deploy, then an inline policy deploy, with
// prior-state UUIDs carried across when the server answers "skipped" with a
// sparse response.
//
// Both uuid assertions are regression guards. A skipped deploy returns an empty
// EntityID, and writing that into state makes Delete short-circuit on
// uuid == "" — the gate is then never archived, and neither is its policy.
func TestAccFianuGate_Update(t *testing.T) {
	stub := newGateStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigGateWithPolicy,
				Check:  resource.TestCheckResourceAttrSet("fianu_gate.security", "uuid"),
			},
			{
				Config: testAccConfigGateUpdated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_gate.security", "detail.description", "Gates production and staging deployments."),
					resource.TestCheckResourceAttrSet("fianu_gate.security", "uuid"),
				),
			},
			// Re-apply the same config: the "skipped" deploy, the exact shape
			// that used to blank the uuid on this resource family.
			{
				Config: testAccConfigGateUpdated,
				Check:  resource.TestCheckResourceAttrSet("fianu_gate.security", "uuid"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	gate, _ := stub.capturedGate.Load().(*fianu_entities.Control)
	if gate == nil {
		t.Fatal("expected the stub to have captured a deployed gate entity, got nil")
	}
	if gate.Detail.Control == nil || gate.Detail.Control.Description == nil ||
		*gate.Detail.Control.Description != "Gates production and staging deployments." {
		t.Errorf("after update, detail.control.description = %+v", gate.Detail.Control)
	}
	// The inline policy must be redeployed alongside the gate, not left at its
	// first-apply content.
	if stub.capturedPolicy.Load() == nil {
		t.Error("the inline policy was not redeployed with the gate")
	}
	if stub.deployHits.Load() < 4 {
		t.Errorf("deploy hits = %d, want at least 4 (gate+policy, twice)", stub.deployHits.Load())
	}
	if got := stub.archiveHits.Load(); got == 0 {
		t.Error("archive hits = 0 — destroy could not reach the entities")
	}
}

const testAccConfigGateUpdated = `
provider "fianu" {}

resource "fianu_gate" "security" {
  path = "test.gate.security"
  name = "Security Gate"

  detail = {
    full_name   = "Production Security Gate"
    display_key = "PSEC"
    description = "Gates production and staging deployments."

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
