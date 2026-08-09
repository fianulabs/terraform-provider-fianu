// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package collection_test

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

// TestAccFianuCollection_Minimal covers the required surface: identity plus
// the parent domain UUID.
func TestAccFianuCollection_Minimal(t *testing.T) {
	stub := newCollectionStub(t)
	defer stub.server.Close()

	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigCollectionMinimal,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_collection.security", "id", "collection/test.collection.security"),
					resource.TestCheckResourceAttrSet("fianu_collection.security", "uuid"),
				),
			},
			{
				Config: testAccConfigCollectionMinimal,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Type != db_vars.EntityTypeCollection {
		t.Errorf("entity type = %q, want collection", got.Type)
	}
	// The server rejects an empty domain, so this is the one field that must
	// always survive the round trip to the wire.
	if got.Detail.Domain != "d0a1b2c3-0000-4000-8000-000000000001" {
		t.Errorf("detail.domain = %q", got.Detail.Domain)
	}
	if got := stub.archiveHits.Load(); got != 1 {
		t.Errorf("expected the destroy step to archive once, got %d", got)
	}
}

const testAccConfigCollectionMinimal = `
provider "fianu" {}

resource "fianu_collection" "security" {
  path = "test.collection.security"
  name = "Security"

  detail = {
    domain = "d0a1b2c3-0000-4000-8000-000000000001"
  }
}
`

// TestAccFianuCollection_FullSpec populates every optional field.
func TestAccFianuCollection_FullSpec(t *testing.T) {
	stub := newCollectionStub(t)
	defer stub.server.Close()

	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigCollectionFull},
			{
				Config: testAccConfigCollectionFull,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Detail.Description != "Application security controls." {
		t.Errorf("description = %q", got.Detail.Description)
	}
	if !got.Detail.InheritDomainPermissions {
		t.Error("inheritDomainPermissions = false, want true")
	}
	if len(got.Detail.Documentation) != 1 || got.Detail.Documentation[0].URL != "https://wiki.example.com/appsec" {
		t.Errorf("documentation = %+v", got.Detail.Documentation)
	}
}

const testAccConfigCollectionFull = `
provider "fianu" {}

resource "fianu_collection" "appsec" {
  path = "test.collection.appsec"
  name = "AppSec"

  detail = {
    description                = "Application security controls."
    domain                     = "d0a1b2c3-0000-4000-8000-000000000002"
    inherit_domain_permissions = true

    documentation = [
      { title = "AppSec wiki", url = "https://wiki.example.com/appsec" },
    ]
  }
}
`

func setEnv(t *testing.T, stub *collectionStub) {
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

// collectionStub fakes Console for the environment resource: deploy captures
// the multipart entity, GET echoes it back so Read doesn't drift, DELETE
// counts archives.
type collectionStub struct {
	server       *httptest.Server
	deployHits   atomic.Int32
	archiveHits  atomic.Int32
	stored       atomic.Value // *transportv1.DeployEntityFileResponse
	capturedVal  atomic.Value // *fianu_entities.Collection
	lastCaptured atomic.Value // *fianu_entities.Collection, never cleared
}

func (s *collectionStub) captured(t *testing.T) *fianu_entities.Collection {
	t.Helper()
	e, _ := s.lastCaptured.Load().(*fianu_entities.Collection)
	if e == nil {
		t.Fatal("no collection captured on the deploy route")
	}
	return e
}

func newCollectionStub(t *testing.T) *collectionStub {
	t.Helper()
	stub := &collectionStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/entities/artifacts/deploy", func(w http.ResponseWriter, r *http.Request) {
		stub.deployHits.Add(1)
		req, raw := decodeDeployRequest(r)

		var e fianu_entities.Collection
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
				EntityID:    "test-collection-uuid",
				Path:        entityPath,
				Name:        e.Name,
				Version:     "1",
				EntityType:  string(db_vars.EntityTypeCollection),
			},
		}
		stub.stored.Store(resp)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/entities/collections/", func(w http.ResponseWriter, r *http.Request) {
		captured, _ := stub.capturedVal.Load().(*fianu_entities.Collection)
		if captured == nil {
			http.NotFound(w, r)
			return
		}
		out := *captured
		out.UUID = "test-collection-uuid"
		out.Type = db_vars.EntityTypeCollection
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
		stub.capturedVal.Store((*fianu_entities.Collection)(nil))
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
