// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package instance_test

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

// TestAccFianuInstance_Minimal covers the smallest instance the server accepts:
// identity plus platform_uuid, which Instance.IsValid requires.
func TestAccFianuInstance_Minimal(t *testing.T) {
	stub := newInstanceStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigInstanceMinimal,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_instance.jira", "id", "instance/test.instance.jira"),
					resource.TestCheckResourceAttr("fianu_instance.jira", "uuid", "test-instance-uuid"),
					resource.TestCheckResourceAttr("fianu_instance.jira", "detail.platform_uuid", "platform-entity-uuid"),
					// Refetch-after-deploy: these come from the read, not the
					// deploy metadata, so their presence proves applyPlan's
					// second call landed.
					resource.TestCheckResourceAttr("fianu_instance.jira", "version.status", "active"),
					resource.TestCheckResourceAttr("fianu_instance.jira", "version.state", "published"),
				),
			},
			{
				Config: testAccConfigInstanceMinimal,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Type != db_vars.EntityTypeInstance {
		t.Errorf("entity type = %q, want instance", got.Type)
	}
	if got.Path != "test.instance.jira" {
		t.Errorf("path = %q, want test.instance.jira", got.Path)
	}
	if got.Detail.PlatformUUID != "platform-entity-uuid" {
		t.Errorf("detail.platformUuid = %q", got.Detail.PlatformUUID)
	}
}

const testAccConfigInstanceMinimal = `
provider "fianu" {}

resource "fianu_instance" "jira" {
  path = "test.instance.jira"
  name = "Acme Jira"

  detail = {
    platform_uuid = "platform-entity-uuid"
  }
}
`

// TestAccFianuInstance_FullSpec exercises every detail attribute and asserts
// the domains land on the wire. Domains are what makes an instance reachable,
// so a dropped one is the failure that matters.
func TestAccFianuInstance_FullSpec(t *testing.T) {
	stub := newInstanceStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigInstanceFull},
			{
				Config: testAccConfigInstanceFull,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Detail.Description != "Acme's production Jira" {
		t.Errorf("description = %q", got.Detail.Description)
	}
	if got.Detail.DisplayKey != "acme-jira" {
		t.Errorf("displayKey = %q, want acme-jira", got.Detail.DisplayKey)
	}
	if len(got.Detail.Domains) != 2 {
		t.Fatalf("domains = %d, want 2", len(got.Detail.Domains))
	}

	api := got.Detail.Domains[0]
	if api.Host != "acme.atlassian.net" {
		t.Errorf("domains[0].host = %q", api.Host)
	}
	if api.Scheme != "https" {
		t.Errorf("domains[0].scheme = %q, want https", api.Scheme)
	}
	if api.Designation != "api" {
		t.Errorf("domains[0].designation = %q, want api", api.Designation)
	}
	if api.BasePath != "/rest/api/3" {
		t.Errorf("domains[0].basePath = %q", api.BasePath)
	}
	if len(api.Utilities) != 2 || api.Utilities[0] != "issues" || api.Utilities[1] != "projects" {
		t.Errorf("domains[0].utilities = %v, want [issues projects]", api.Utilities)
	}
	if api.ProxyURL != "http://egress.acme.internal:3128" {
		t.Errorf("domains[0].proxyUrl = %q", api.ProxyURL)
	}
	if !api.Cache {
		t.Error("domains[0].cache = false, want true")
	}

	ui := got.Detail.Domains[1]
	if ui.Designation != "ui" {
		t.Errorf("domains[1].designation = %q, want ui", ui.Designation)
	}
	// An unset repeated field must stay nil, not become an empty slice: the
	// server skips the utilities insert entirely when there are none.
	if ui.Utilities != nil {
		t.Errorf("domains[1].utilities = %v, want nil for an unset field", ui.Utilities)
	}

	// Domain UUIDs are per-version and server-assigned. The provider must not
	// send one, or it would pin a value the server re-stamps anyway.
	for i, d := range got.Detail.Domains {
		if d.UUID != "" {
			t.Errorf("domains[%d].uuid = %q, want empty — domain uuids are server-assigned", i, d.UUID)
		}
	}
}

const testAccConfigInstanceFull = `
provider "fianu" {}

resource "fianu_instance" "jira" {
  path = "test.instance.jira"
  name = "Acme Jira"

  detail = {
    description   = "Acme's production Jira"
    display_key   = "acme-jira"
    platform_uuid = "platform-entity-uuid"

    domains = [
      {
        host         = "acme.atlassian.net"
        scheme       = "https"
        designation  = "api"
        base_path    = "/rest/api/3"
        utilities    = ["issues", "projects"]
        display_name = "Acme Jira API"
        display_key  = "acme-jira-api"
        description  = "REST v3"
        proxy_url    = "http://egress.acme.internal:3128"
        cache        = true
      },
      {
        host        = "acme.atlassian.net"
        scheme      = "https"
        designation = "ui"
      },
    ]
  }
}
`

// TestAccFianuInstance_DomainsAreNotChildren pins the data-model decision.
// Domains ride the detail; `children` is for real entity-to-entity edges, and
// putting domains there would make each one look like an addressable entity.
func TestAccFianuInstance_DomainsAreNotChildren(t *testing.T) {
	stub := newInstanceStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigInstanceFull},
		},
	})

	got := stub.captured(t)
	if len(got.Children) != 0 {
		t.Errorf("children = %d entries, want 0 — domains belong on detail, not the entity graph", len(got.Children))
	}
	if len(got.Detail.Domains) != 2 {
		t.Errorf("detail.domains = %d, want 2", len(got.Detail.Domains))
	}
}

// TestAccFianuInstance_Update proves an in-place change round-trips: the second
// deploy is not swallowed by the idempotency gate, and a removed domain is
// actually removed rather than merged.
func TestAccFianuInstance_Update(t *testing.T) {
	stub := newInstanceStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigInstanceFull,
				Check:  resource.TestCheckResourceAttr("fianu_instance.jira", "detail.domains.#", "2"),
			},
			{
				Config: testAccConfigInstanceUpdated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_instance.jira", "detail.domains.#", "1"),
					resource.TestCheckResourceAttr("fianu_instance.jira", "detail.description", "Acme's Jira, sandbox"),
				),
			},
		},
	})

	got := stub.captured(t)
	if len(got.Detail.Domains) != 1 {
		t.Fatalf("after update, domains = %d, want 1", len(got.Detail.Domains))
	}
	if got.Detail.Domains[0].Host != "sandbox.atlassian.net" {
		t.Errorf("after update, domains[0].host = %q", got.Detail.Domains[0].Host)
	}
	if stub.deployHits.Load() < 2 {
		t.Errorf("deploy hits = %d, want at least 2 — the update never reached the server", stub.deployHits.Load())
	}
}

const testAccConfigInstanceUpdated = `
provider "fianu" {}

resource "fianu_instance" "jira" {
  path = "test.instance.jira"
  name = "Acme Jira"

  detail = {
    description   = "Acme's Jira, sandbox"
    display_key   = "acme-jira"
    platform_uuid = "platform-entity-uuid"

    domains = [
      {
        host        = "sandbox.atlassian.net"
        scheme      = "https"
        designation = "api"
      },
    ]
  }
}
`

// TestAccFianuInstance_Import proves the composite ID round-trips through
// `terraform import`.
func TestAccFianuInstance_Import(t *testing.T) {
	stub := newInstanceStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigInstanceMinimal},
			{
				Config:            testAccConfigInstanceMinimal,
				ResourceName:      "fianu_instance.jira",
				ImportState:       true,
				ImportStateId:     "instance/test.instance.jira",
				ImportStateVerify: true,
				// detail stays user-authored — Read hydrates the envelope plus
				// display_key, because the server re-stamps domain uuids on
				// every write and returns domains in table order.
				ImportStateVerifyIgnore: []string{"detail"},
			},
		},
	})
}

// TestAccFianuInstance_RejectsUnknownScheme pins the plan-time guard. An
// unvalidated scheme deploys fine and fails at request time inside whatever job
// reaches for the domain first, a long way from the config that caused it.
func TestAccFianuInstance_RejectsUnknownScheme(t *testing.T) {
	stub := newInstanceStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigInstanceBadScheme,
				ExpectError: regexp.MustCompile(`Attribute detail\.domains\[0\]\.scheme value must be one of`),
			},
		},
	})
}

const testAccConfigInstanceBadScheme = `
provider "fianu" {}

resource "fianu_instance" "jira" {
  path = "test.instance.jira"
  name = "Acme Jira"

  detail = {
    platform_uuid = "platform-entity-uuid"

    domains = [
      {
        host        = "acme.atlassian.net"
        scheme      = "ftp"
        designation = "api"
      },
    ]
  }
}
`

func setEnv(t *testing.T, stub *instanceStub) {
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

// instanceStub fakes Console for the instance resource: deploy captures the
// multipart entity, GET echoes it back so Read doesn't drift, DELETE counts
// archives.
type instanceStub struct {
	server       *httptest.Server
	deployHits   atomic.Int32
	archiveHits  atomic.Int32
	archivedPath atomic.Value // string
	stored       atomic.Value // *transportv1.DeployEntityFileResponse
	capturedVal  atomic.Value // *fianu_entities.Instance
	lastCaptured atomic.Value // *fianu_entities.Instance, never cleared
}

func (s *instanceStub) captured(t *testing.T) *fianu_entities.Instance {
	t.Helper()
	e, _ := s.lastCaptured.Load().(*fianu_entities.Instance)
	if e == nil {
		t.Fatal("no instance captured on the deploy route")
	}
	return e
}

func newInstanceStub(t *testing.T) *instanceStub {
	t.Helper()
	stub := &instanceStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/entities/artifacts/deploy", func(w http.ResponseWriter, r *http.Request) {
		stub.deployHits.Add(1)
		req, raw := decodeDeployRequest(r)

		var e fianu_entities.Instance
		if err := json.Unmarshal(raw, &e); err == nil {
			stub.capturedVal.Store(&e)
			stub.lastCaptured.Store(&e)
		}

		// Reject anything the real endpoint would: the deploy allowlist is
		// keyed on General.entityType, so a resource sending the wrong one
		// would 400 in production but pass a stub that ignores it.
		if req.General.EntityType == nil || *req.General.EntityType != string(db_vars.EntityTypeInstance) {
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
				EntityID:    "test-instance-uuid",
				Path:        entityPath,
				Name:        e.Name,
				Version:     "1",
				EntityType:  string(db_vars.EntityTypeInstance),
			},
		}
		stub.stored.Store(resp)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// The canonical read lives under /api/integrations/, not /api/entities/ —
	// consulta owns the /api/entities/instances path with its reporting view.
	mux.HandleFunc("/api/integrations/instances/", func(w http.ResponseWriter, r *http.Request) {
		captured, _ := stub.capturedVal.Load().(*fianu_entities.Instance)
		if captured == nil {
			http.NotFound(w, r)
			return
		}
		out := *captured
		out.UUID = "test-instance-uuid"
		out.Type = db_vars.EntityTypeInstance
		out.Version.Semantic = "1"
		out.Version.UUID = "version-uuid"
		out.Version.Status = db_vars.EntityStatusActive
		out.Version.State = db_vars.EntityStatePublished
		// The server defaults displayKey from the path and stamps a fresh uuid
		// on every domain. Echoing that back is what makes the empty-plan check
		// meaningful: it proves the provider does not hydrate domains into
		// state and then see drift.
		if out.Detail.DisplayKey == "" {
			out.Detail.DisplayKey = out.Path
		}
		// Copy the slice before stamping. `out := *captured` is shallow, so
		// writing through out.Detail.Domains would edit the captured entity
		// and the wire assertions would be checking the echo, not the deploy.
		domains := make([]fianu_entities.InstanceDomain, len(out.Detail.Domains))
		copy(domains, out.Detail.Domains)
		for i := range domains {
			domains[i].UUID = "server-assigned-domain-uuid"
		}
		out.Detail.Domains = domains
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("/api/integrations/archive/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		stub.archiveHits.Add(1)
		stub.archivedPath.Store(r.URL.Path)
		stub.capturedVal.Store((*fianu_entities.Instance)(nil))
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
