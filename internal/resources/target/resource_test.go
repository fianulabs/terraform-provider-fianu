// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package target_test

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

// TestAccFianuTarget_Minimal covers the required surface: identity plus at
// least one environment binding, which the server enforces.
func TestAccFianuTarget_Minimal(t *testing.T) {
	stub := newTargetStub(t)
	defer stub.server.Close()

	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigTargetMinimal,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_target.eks", "id", "target/test.target.eks_prod"),
					resource.TestCheckResourceAttrSet("fianu_target.eks", "uuid"),
				),
			},
			{
				Config: testAccConfigTargetMinimal,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Type != db_vars.EntityTypeTarget {
		t.Errorf("entity type = %q, want target", got.Type)
	}
	// Environments are satellites of the entity, NOT part of detail. If
	// TargetWithEnvironment's custom (Un)MarshalJSON ever regresses, this is
	// the assertion that catches it — StandardEntity's own unmarshal would
	// swallow the whole blob and drop them.
	if len(got.Environments) != 1 {
		t.Fatalf("expected 1 environment ref, got %d", len(got.Environments))
	}
	if got.Environments[0].Environment != "test.env.prod" {
		t.Errorf("environments[0].environment = %q, want the authored path", got.Environments[0].Environment)
	}
	if got := stub.archiveHits.Load(); got != 1 {
		t.Errorf("expected the destroy step to archive once, got %d", got)
	}
}

const testAccConfigTargetMinimal = `
provider "fianu" {}

resource "fianu_target" "eks" {
  path = "test.target.eks_prod"
  name = "EKS Production"

  detail = {
    cloud_provider = "AWS"
    type           = "kubernetes"
  }

  environments = [
    { environment = "test.env.prod" },
  ]
}
`

// TestAccFianuTarget_FullSpec populates every field, including the three
// satellites that sit beside detail rather than inside it.
func TestAccFianuTarget_FullSpec(t *testing.T) {
	stub := newTargetStub(t)
	defer stub.server.Close()

	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigTargetFull},
			{
				Config: testAccConfigTargetFull,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Detail.CloudProvider != "GCP" {
		t.Errorf("cloudProvider = %q", got.Detail.CloudProvider)
	}
	// HCL calls it `type`; the wire field is TargetType behind json:"type".
	if got.Detail.TargetType != "serverless" {
		t.Errorf("detail type = %q, want serverless", got.Detail.TargetType)
	}
	if got.Detail.Region != "us-central1" {
		t.Errorf("region = %q", got.Detail.Region)
	}
	if len(got.Detail.Tags) != 2 {
		t.Errorf("tags = %v", got.Detail.Tags)
	}
	if len(got.Environments) != 2 {
		t.Fatalf("expected 2 environment refs, got %d", len(got.Environments))
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "cloudrun-prod" {
		t.Errorf("aliases = %v", got.Aliases)
	}
	if len(got.Documentation) != 1 || got.Documentation[0].Title != "Deploy runbook" {
		t.Errorf("documentation = %+v", got.Documentation)
	}
}

const testAccConfigTargetFull = `
provider "fianu" {}

resource "fianu_target" "cloudrun" {
  path = "test.target.cloudrun_prod"
  name = "Cloud Run Production"

  detail = {
    description    = "Production Cloud Run services."
    cloud_provider = "GCP"
    type           = "serverless"
    service        = "cloudrun"
    region         = "us-central1"
    solution       = "checkout"
    tags           = ["prod", "pci"]
  }

  environments = [
    { environment = "test.env.prod" },
    { environment = "test.env.canary" },
  ]

  aliases = ["cloudrun-prod"]

  documentation = [
    { title = "Deploy runbook", url = "https://runbooks.example.com/cloudrun" },
  ]
}
`

func setEnv(t *testing.T, stub *targetStub) {
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

// targetStub fakes Console for the environment resource: deploy captures
// the multipart entity, GET echoes it back so Read doesn't drift, DELETE
// counts archives.
type targetStub struct {
	server       *httptest.Server
	deployHits   atomic.Int32
	archiveHits  atomic.Int32
	stored       atomic.Value // *transportv1.DeployEntityFileResponse
	capturedVal  atomic.Value // *fianu_entities.TargetWithEnvironment
	lastCaptured atomic.Value // *fianu_entities.TargetWithEnvironment, never cleared
}

func (s *targetStub) captured(t *testing.T) *fianu_entities.TargetWithEnvironment {
	t.Helper()
	e, _ := s.lastCaptured.Load().(*fianu_entities.TargetWithEnvironment)
	if e == nil {
		t.Fatal("no target captured on the deploy route")
	}
	return e
}

func newTargetStub(t *testing.T) *targetStub {
	t.Helper()
	stub := &targetStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/entities/artifacts/deploy", func(w http.ResponseWriter, r *http.Request) {
		stub.deployHits.Add(1)
		req, raw := decodeDeployRequest(r)

		var e fianu_entities.TargetWithEnvironment
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
				EntityID:    "test-target-uuid",
				Path:        entityPath,
				Name:        e.Name,
				Version:     "1",
				EntityType:  string(db_vars.EntityTypeTarget),
			},
		}
		stub.stored.Store(resp)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/entities/targets/", func(w http.ResponseWriter, r *http.Request) {
		captured, _ := stub.capturedVal.Load().(*fianu_entities.TargetWithEnvironment)
		if captured == nil {
			http.NotFound(w, r)
			return
		}
		out := *captured
		out.UUID = "test-target-uuid"
		out.Type = db_vars.EntityTypeTarget
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
		stub.capturedVal.Store((*fianu_entities.TargetWithEnvironment)(nil))
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

// TestAccFianuTarget_Import proves the composite ID round-trips through
// `terraform import`. targetModel.Detail is a value type, so ImportState has to
// seed it — the framework cannot convert null into a non-pointer struct and the
// post-import Read fails before it starts.
func TestAccFianuTarget_Import(t *testing.T) {
	stub := newTargetStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigTargetMinimal},
			{
				Config:            testAccConfigTargetMinimal,
				ResourceName:      "fianu_target.eks",
				ImportState:       true,
				ImportStateId:     "target/test.target.eks_prod",
				ImportStateVerify: true,
				// detail and environments stay user-authored — Read hydrates the
				// envelope only, and the server resolves environment refs to
				// UUIDs.
				ImportStateVerifyIgnore: []string{"detail", "environments"},
			},
		},
	})
}

// TestAccFianuTarget_Update covers the second-apply path: the update reaches
// the server, the new values land on the wire, and the resource keeps its uuid
// so destroy can still archive it.
//
// The uuid assertion is the regression guard. The server returns an empty
// EntityID with action="skipped" when content is unchanged; Hydrate used to
// write that empty value into state, after which Delete short-circuits on
// uuid == "" and the target is never archived. Fixed in 0.3.0 — fianu_target
// was one of the named resources, and nothing tested it.
func TestAccFianuTarget_Update(t *testing.T) {
	stub := newTargetStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigTargetMinimal,
				Check:  resource.TestCheckResourceAttrSet("fianu_target.eks", "uuid"),
			},
			{
				Config: testAccConfigTargetUpdated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_target.eks", "detail.region", "us-west-2"),
					resource.TestCheckResourceAttrSet("fianu_target.eks", "uuid"),
				),
			},
			// Re-apply the same config: the "skipped" deploy, which is the
			// exact shape that used to blank the uuid.
			{
				Config: testAccConfigTargetUpdated,
				Check:  resource.TestCheckResourceAttrSet("fianu_target.eks", "uuid"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Detail.Region != "us-west-2" {
		t.Errorf("after update, region = %q", got.Detail.Region)
	}
	if stub.deployHits.Load() < 2 {
		t.Errorf("deploy hits = %d, want at least 2 — the update never reached the server", stub.deployHits.Load())
	}
	if got := stub.archiveHits.Load(); got != 1 {
		t.Errorf("archive hits = %d, want 1 — destroy could not reach the entity", got)
	}
}

const testAccConfigTargetUpdated = `
provider "fianu" {}

resource "fianu_target" "eks" {
  path = "test.target.eks_prod"
  name = "EKS Production"

  detail = {
    cloud_provider = "AWS"
    type           = "kubernetes"
    region         = "us-west-2"
  }

  environments = [
    { environment = "test.env.prod" },
  ]
}
`
