// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package policy_test

import (
	"encoding/json"
	"fmt"
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

// TestAccFianuPolicy_Minimal exercises the bare envelope + required Detail
// fields (type + control.path + a single apply variation). Asserts:
//
//   - Create — deploy hits the stub Console with the expected wire payload
//   - Read   — FetchPolicy populates envelope state without drift
//   - Plan   — re-running plan after apply yields zero diff (idempotency
//     contract end-to-end through the framework)
func TestAccFianuPolicy_Minimal(t *testing.T) {
	stub := newPolicyStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigMinimalPolicy,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_policy.example", "path", "test.policy.basic"),
					resource.TestCheckResourceAttr("fianu_policy.example", "name", "Basic Test Policy"),
					resource.TestCheckResourceAttr("fianu_policy.example", "detail.type", "standard"),
					resource.TestCheckResourceAttr("fianu_policy.example", "detail.control.path", "test.control.basic"),
					resource.TestCheckResourceAttr("fianu_policy.example", "id", "policy/test.policy.basic"),
				),
			},
			{
				Config: testAccConfigMinimalPolicy,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	captured, _ := stub.capturedEntity.Load().(*fianu_entities.Policy)
	if captured == nil {
		t.Fatalf("expected the stub to have captured a deployed policy entity, got nil")
	}
	if captured.Detail.Control.Path != "test.control.basic" {
		t.Errorf("control.path = %q, want %q", captured.Detail.Control.Path, "test.control.basic")
	}
	if string(captured.Detail.Type) != "standard" {
		t.Errorf("policy.type = %q, want %q", captured.Detail.Type, "standard")
	}
	if len(captured.Detail.Variations) != 1 {
		t.Fatalf("expected 1 variation, got %d", len(captured.Detail.Variations))
	}
	if string(captured.Detail.Variations[0].PolicyEffect) != "apply" {
		t.Errorf("variation[0].effect = %q, want apply", captured.Detail.Variations[0].PolicyEffect)
	}
	required, ok := captured.Detail.Variations[0].Policy["required"]
	if !ok || required != true {
		t.Errorf("variation[0].policy.required = %v ok=%v, want true", required, ok)
	}
}

const testAccConfigMinimalPolicy = `
provider "fianu" {}

resource "fianu_policy" "example" {
  path = "test.policy.basic"
  name = "Basic Test Policy"

  detail = {
    type = "standard"
    control = {
      path = "test.control.basic"
    }
    variations = [
      {
        effect   = "apply"
        priority = 0
        policy   = jsonencode({ required = true })
      },
    ]
  }
}
`

// TestAccFianuPolicy_FullSpec adds the override block and a multi-variation
// list to exercise the full HCL surface.
func TestAccFianuPolicy_FullSpec(t *testing.T) {
	stub := newPolicyStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigFullSpecPolicy},
			{
				Config: testAccConfigFullSpecPolicy,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	captured, _ := stub.capturedEntity.Load().(*fianu_entities.Policy)
	if captured == nil {
		t.Fatalf("expected captured entity, got nil")
	}
	if len(captured.Detail.Variations) != 2 {
		t.Fatalf("expected 2 variations, got %d", len(captured.Detail.Variations))
	}
	if string(captured.Detail.Variations[1].PolicyEffect) != "exempt" {
		t.Errorf("second variation effect = %q, want exempt", captured.Detail.Variations[1].PolicyEffect)
	}
	if captured.Detail.Override == nil {
		t.Fatal("override block should be set")
	}
	if got := captured.Detail.Override.Asset.Types; len(got) != 1 || got[0] != "repository" {
		t.Errorf("override.asset.types = %v, want [repository]", got)
	}
}

// TestAccFianuPolicy_CriteriaPopulatesExprSource regresses a bug where the
// provider populated PolicyAssetGroupExpression.Expr (raw) instead of
// ExprSource (parsed CEL). The server's PolicyAssetGroup validator reads
// ExprSource exclusively and rejects criteria with empty ExprSource as
// "invalid criteria" — so the wire must carry the CEL string in ExprSource.
func TestAccFianuPolicy_CriteriaPopulatesExprSource(t *testing.T) {
	stub := newPolicyStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	cfg := `
provider "fianu" {}
resource "fianu_policy" "criteria" {
  path = "test.policy.criteria"
  name = "Criteria Test"
  detail = {
    type = "standard"
    control = { path = "test.control.basic" }
    variations = [
      {
        criteria = {
          expressions = [
            { expression = "asset.scm.repository startsWith 'prod-'" },
          ]
        }
        policy = jsonencode({ required = true })
      },
    ]
  }
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps:                    []resource.TestStep{{Config: cfg}},
	})

	captured, _ := stub.capturedEntity.Load().(*fianu_entities.Policy)
	if captured == nil {
		t.Fatal("no entity captured")
	}
	if len(captured.Detail.Variations) != 1 {
		t.Fatalf("expected 1 variation, got %d", len(captured.Detail.Variations))
	}
	crit := captured.Detail.Variations[0].Criteria
	if crit == nil {
		t.Fatal("variation should have criteria")
	}
	if len(crit.Expressions) != 1 {
		t.Fatalf("expected 1 expression, got %d", len(crit.Expressions))
	}
	raw := "asset.scm.repository startsWith 'prod-'"
	// ExprDisplay carries the raw user CEL; ExprSource carries the parsed
	// canonical form (with $ prefix + .(type) casts) the server validator
	// expects to compile.
	if got := crit.Expressions[0].ExprDisplay; got != raw {
		t.Errorf("ExprDisplay = %q, want %q", got, raw)
	}
	if got := crit.Expressions[0].ExprSource; got == "" {
		t.Error("ExprSource is empty — provider should have populated it via cel.ParseExpression")
	} else if got == raw {
		t.Errorf("ExprSource = %q, expected the *parsed* canonical form (with $ prefix), got the raw form", got)
	}
	if crit.Expressions[0].Expr != nil {
		t.Errorf("Expr should be nil (provider populates ExprSource instead), got %q", *crit.Expressions[0].Expr)
	}
}

const testAccConfigFullSpecPolicy = `
provider "fianu" {}

resource "fianu_policy" "full" {
  path = "test.policy.full"
  name = "Full Spec Test Policy"

  detail = {
    type = "standard"
    control = {
      path = "test.control.basic"
    }
    variations = [
      {
        effect   = "apply"
        priority = 0
        policy   = jsonencode({ required = true, vulnerabilities = { critical = { maximum = 0 } } })
      },
      {
        effect   = "exempt"
        priority = 100
        locked   = true
        policy   = jsonencode({})
      },
    ]
    override = {
      asset = {
        types = ["repository"]
      }
    }
  }
}
`

// protoV6Factories returns the protocol-v6 factories the testing framework
// requires. Constructed fresh per test run.
func protoV6Factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"fianu": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

// policyStub is the policy-flavoured counterpart to the control package's
// newConsoleStub. Mounts the four routes the provider hits:
//
//   - POST   /api/entities/artifacts/deploy           (Create/Update — captures the entity)
//   - GET    /api/entities/policies/{key}             (Read — echoes the captured entity back)
//   - DELETE /api/entities/archive/policy/{uuid}      (Delete)
//   - POST   /api/entities/artifacts/test             (unused by policy resource — included for symmetry)
type policyStub struct {
	server         *httptest.Server
	deployHits     atomic.Int32
	fetchHits      atomic.Int32
	archiveHits    atomic.Int32
	stored         atomic.Value // *transportv1.DeployEntityFileResponse
	capturedEntity atomic.Value // *fianu_entities.Policy
}

func newPolicyStub(t *testing.T) *policyStub {
	t.Helper()
	stub := &policyStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/entities/artifacts/deploy", func(w http.ResponseWriter, r *http.Request) {
		stub.deployHits.Add(1)

		req, _, entity := decodeMultipartPolicy(r)
		path := ""
		if req.General.Path != nil {
			path = *req.General.Path
		}
		entityName := ""
		if entity != nil {
			stub.capturedEntity.Store(entity)
			entityName = entity.Name
		}

		// Mirror the server's idempotency gate: a repeat deploy with the
		// same content hash returns action="skipped".
		action := "created"
		if prior := stub.stored.Load(); prior != nil {
			pr := prior.(*transportv1.DeployEntityFileResponse)
			if pr.Metadata != nil && pr.Metadata.ContentHash == r.Header.Get("X-Fianu-CI-System-Hash") {
				action = "skipped"
			} else {
				action = "updated"
			}
		}

		respName := entityName
		if respName == "" {
			respName = "Basic Test Policy"
		}
		resp := &transportv1.DeployEntityFileResponse{
			Message: "ok",
			Metadata: &transportv1.DeploymentMetadata{
				Action:      action,
				ContentHash: r.Header.Get("X-Fianu-CI-System-Hash"),
				EntityID:    "test-policy-uuid",
				Path:        path,
				Name:        respName,
				Version:     "1",
				EntityType:  "policy",
			},
		}
		stub.stored.Store(resp)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Echo back the captured Policy as-deployed so Read doesn't drift
	// against the user's HCL. Matches the shape FetchPolicy now returns.
	mux.HandleFunc("/api/entities/policies/", func(w http.ResponseWriter, r *http.Request) {
		stub.fetchHits.Add(1)
		w.Header().Set("Content-Type", "application/json")

		captured, _ := stub.capturedEntity.Load().(*fianu_entities.Policy)
		if captured == nil {
			http.NotFound(w, r)
			return
		}

		out := *captured
		out.UUID = "test-policy-uuid"
		out.StandardEntity.Type = db_vars.EntityTypePolicy
		out.Version.Semantic = "1"
		out.Version.UUID = "version-uuid"
		out.Version.Status = "active"
		out.Version.State = "published"
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

// decodeMultipartPolicy parses the multipart deploy request and decodes the
// file part into a *fianu_entities.Policy. Mirrors the control stub's
// decodeMultipartEntity.
func decodeMultipartPolicy(r *http.Request) (transportv1.DeployEntityFileRequest, []byte, *fianu_entities.Policy) {
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

	var p fianu_entities.Policy
	if err := json.Unmarshal(raw, &p); err != nil {
		return req, raw, nil
	}
	return req, raw, &p
}

// Avoid an unused-import diagnostic when building without exercising the
// stub's full surface.
var _ = fmt.Sprintf
