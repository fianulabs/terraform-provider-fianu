// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package environment_test

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

// TestAccFianuEnvironment_Minimal covers identity only — the smallest
// environment the server accepts.
func TestAccFianuEnvironment_Minimal(t *testing.T) {
	stub := newEnvironmentStub(t)
	defer stub.server.Close()

	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigEnvironmentMinimal,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_environment.dev", "id", "environment/test.env.dev"),
					resource.TestCheckResourceAttrSet("fianu_environment.dev", "uuid"),
				),
			},
			{
				Config: testAccConfigEnvironmentMinimal,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Type != db_vars.EntityTypeEnvironment {
		t.Errorf("entity type = %q, want environment", got.Type)
	}
	if got.Path != "test.env.dev" {
		t.Errorf("path = %q", got.Path)
	}
	if got.Detail.Matching != nil {
		t.Errorf("matching should be nil when unset, got %+v", got.Detail.Matching)
	}
	if got := stub.archiveHits.Load(); got != 1 {
		t.Errorf("expected the destroy step to archive once, got %d", got)
	}
}

const testAccConfigEnvironmentMinimal = `
provider "fianu" {}

resource "fianu_environment" "dev" {
  path = "test.env.dev"
  name = "Development"

  detail = {
    description = "Shared development environment."
  }
}
`

// TestAccFianuEnvironment_WithMatching asserts the CEL matcher reaches the
// wire in the canonical form the server compiles — the same pre-parse the
// policy and gate resources rely on.
func TestAccFianuEnvironment_WithMatching(t *testing.T) {
	stub := newEnvironmentStub(t)
	defer stub.server.Close()

	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigEnvironmentMatching},
			{
				Config: testAccConfigEnvironmentMatching,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Detail.Matching == nil {
		t.Fatal("detail.matching is nil — the CEL matcher never reached the wire")
	}
	if got.Detail.Matching.Asset == nil || string(got.Detail.Matching.Asset.Type) != "repository" {
		t.Errorf("matching.asset = %+v", got.Detail.Matching.Asset)
	}
	if len(got.Detail.Matching.Expressions) != 1 {
		t.Fatalf("expected 1 matching expression, got %d", len(got.Detail.Matching.Expressions))
	}
	if got.Detail.Matching.Expressions[0].ExprSource == "" {
		t.Errorf("matching expression was not pre-parsed into exprSource: %+v", got.Detail.Matching.Expressions[0])
	}
	if len(got.Detail.Documentation) != 1 || got.Detail.Documentation[0].Title != "Runbook" {
		t.Errorf("documentation = %+v", got.Detail.Documentation)
	}
}

const testAccConfigEnvironmentMatching = `
provider "fianu" {}

resource "fianu_environment" "prod" {
  path = "test.env.prod"
  name = "Production"

  detail = {
    description = "Production environment."

    documentation = [
      { title = "Runbook", url = "https://runbooks.example.com/prod" },
    ]

    matching = {
      asset = { type = "repository" }
      expressions = [
        { expression = "asset.scm.repository startsWith 'prod-'" },
      ]
    }
  }
}
`

func setEnv(t *testing.T, stub *environmentStub) {
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

// environmentStub fakes Console for the environment resource: deploy captures
// the multipart entity, GET echoes it back so Read doesn't drift, DELETE
// counts archives.
type environmentStub struct {
	server       *httptest.Server
	deployHits   atomic.Int32
	archiveHits  atomic.Int32
	stored       atomic.Value // *transportv1.DeployEntityFileResponse
	capturedVal  atomic.Value // *fianu_entities.Environment
	lastCaptured atomic.Value // *fianu_entities.Environment, never cleared
}

func (s *environmentStub) captured(t *testing.T) *fianu_entities.Environment {
	t.Helper()
	e, _ := s.lastCaptured.Load().(*fianu_entities.Environment)
	if e == nil {
		t.Fatal("no environment captured on the deploy route")
	}
	return e
}

func newEnvironmentStub(t *testing.T) *environmentStub {
	t.Helper()
	stub := &environmentStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/entities/artifacts/deploy", func(w http.ResponseWriter, r *http.Request) {
		stub.deployHits.Add(1)
		req, raw := decodeDeployRequest(r)

		var e fianu_entities.Environment
		if err := json.Unmarshal(raw, &e); err == nil {
			stub.capturedVal.Store(&e)
			stub.lastCaptured.Store(&e)
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
				EntityID:    "test-environment-uuid",
				Path:        entityPath,
				Name:        e.Name,
				Version:     "1",
				EntityType:  string(db_vars.EntityTypeEnvironment),
			},
		}
		stub.stored.Store(resp)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/entities/environments/", func(w http.ResponseWriter, r *http.Request) {
		captured, _ := stub.capturedVal.Load().(*fianu_entities.Environment)
		if captured == nil {
			http.NotFound(w, r)
			return
		}
		out := *captured
		out.UUID = "test-environment-uuid"
		out.Type = db_vars.EntityTypeEnvironment
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
		stub.capturedVal.Store((*fianu_entities.Environment)(nil))
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

// TestAccFianuEnvironment_Import proves the composite ID round-trips through
// `terraform import`. environmentModel.Detail is a value type, so ImportState
// has to seed it — the framework cannot convert null into a non-pointer struct
// and the post-import Read fails before it starts.
func TestAccFianuEnvironment_Import(t *testing.T) {
	stub := newEnvironmentStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigEnvironmentMinimal},
			{
				Config:            testAccConfigEnvironmentMinimal,
				ResourceName:      "fianu_environment.dev",
				ImportState:       true,
				ImportStateId:     "environment/test.env.dev",
				ImportStateVerify: true,
				// detail stays user-authored — Read hydrates the envelope only.
				ImportStateVerifyIgnore: []string{"detail"},
			},
		},
	})
}

// TestAccFianuEnvironment_Update covers the second-apply path: the update
// reaches the server, the new values land on the wire, and the resource keeps
// its uuid so destroy can still archive it.
//
// The uuid assertion is the regression guard. The server returns an empty
// EntityID with action="skipped" when content is unchanged; Hydrate used to
// write that empty value into state, after which Delete short-circuits on
// uuid == "" and the environment is never archived. Fixed in 0.3.0 —
// fianu_environment was one of the named resources, and nothing tested it.
func TestAccFianuEnvironment_Update(t *testing.T) {
	stub := newEnvironmentStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigEnvironmentMinimal,
				Check:  resource.TestCheckResourceAttrSet("fianu_environment.dev", "uuid"),
			},
			{
				Config: testAccConfigEnvironmentUpdated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_environment.dev", "detail.description", "Updated development environment."),
					resource.TestCheckResourceAttrSet("fianu_environment.dev", "uuid"),
				),
			},
			// Re-apply the same config: the "skipped" deploy, which is the
			// exact shape that used to blank the uuid.
			{
				Config: testAccConfigEnvironmentUpdated,
				Check:  resource.TestCheckResourceAttrSet("fianu_environment.dev", "uuid"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Detail.Description == nil || *got.Detail.Description != "Updated development environment." {
		t.Errorf("after update, description = %v", got.Detail.Description)
	}
	if stub.deployHits.Load() < 2 {
		t.Errorf("deploy hits = %d, want at least 2 — the update never reached the server", stub.deployHits.Load())
	}
	if got := stub.archiveHits.Load(); got != 1 {
		t.Errorf("archive hits = %d, want 1 — destroy could not reach the entity", got)
	}
}

const testAccConfigEnvironmentUpdated = `
provider "fianu" {}

resource "fianu_environment" "dev" {
  path = "test.env.dev"
  name = "Development"

  detail = {
    description = "Updated development environment."
  }
}
`
