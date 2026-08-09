// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package tool_test

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

// TestAccFianuTool_Minimal covers the smallest tool the server accepts:
// identity plus tool_version, which Tool.IsValid requires.
func TestAccFianuTool_Minimal(t *testing.T) {
	stub := newToolStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigToolMinimal,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_tool.scanner", "id", "tool/test.tool.scanner"),
					resource.TestCheckResourceAttr("fianu_tool.scanner", "uuid", "test-tool-uuid"),
					resource.TestCheckResourceAttr("fianu_tool.scanner", "detail.tool_version", "8.2.0"),
					// Refetch-after-deploy: these come from the read, not the
					// deploy metadata, so their presence proves applyPlan's
					// second call landed.
					resource.TestCheckResourceAttr("fianu_tool.scanner", "version.status", "active"),
					resource.TestCheckResourceAttr("fianu_tool.scanner", "version.state", "published"),
				),
			},
			{
				Config: testAccConfigToolMinimal,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Type != db_vars.EntityTypeTool {
		t.Errorf("entity type = %q, want tool", got.Type)
	}
	if got.Path != "test.tool.scanner" {
		t.Errorf("path = %q, want test.tool.scanner", got.Path)
	}
	if got.Detail.ToolVersion != "8.2.0" {
		t.Errorf("detail.toolVersion = %q, want 8.2.0", got.Detail.ToolVersion)
	}
}

const testAccConfigToolMinimal = `
provider "fianu" {}

resource "fianu_tool" "scanner" {
  path = "test.tool.scanner"
  name = "Checkmarx SAST"

  detail = {
    tool_version = "8.2.0"
  }
}
`

// TestAccFianuTool_FullSpec exercises every detail attribute, and asserts the
// sources graph lands on the wire — that is the part the server validates the
// control graph against, so a dropped edge is the failure that matters.
func TestAccFianuTool_FullSpec(t *testing.T) {
	stub := newToolStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigToolFull},
			{
				Config: testAccConfigToolFull,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Detail.Description != "Static analysis scanner" {
		t.Errorf("description = %q", got.Detail.Description)
	}
	if got.Detail.Key != "checkmarx" {
		t.Errorf("key = %q, want checkmarx", got.Detail.Key)
	}
	if got.Detail.ToolType != "sast" {
		t.Errorf("toolType = %q, want sast", got.Detail.ToolType)
	}
	if len(got.Detail.Sources.Produces) != 1 {
		t.Fatalf("produces = %d entries, want 1", len(got.Detail.Sources.Produces))
	}
	produce := got.Detail.Sources.Produces[0]
	if produce.Path != "checkmarx.sast.results" {
		t.Errorf("produces[0].path = %q", produce.Path)
	}
	if produce.Note != "occurrence" {
		t.Errorf("produces[0].note = %q, want occurrence", produce.Note)
	}
	if produce.Integration.Name == nil || *produce.Integration.Name != "checkmarx" {
		t.Errorf("produces[0].integration.name = %v, want checkmarx", produce.Integration.Name)
	}
	if produce.Integration.Type == nil || *produce.Integration.Type != "tool" {
		t.Errorf("produces[0].integration.type = %v, want tool", produce.Integration.Type)
	}
	// An unset integration field must stay nil, not become an empty string:
	// the server treats "" and absent differently when resolving references.
	if produce.Integration.EntityId != nil {
		t.Errorf("produces[0].integration.entityId = %v, want nil for an unset field", *produce.Integration.EntityId)
	}
	if produce.Schema == nil {
		t.Fatal("produces[0].schema was dropped; the jsonencode string did not reach the wire")
	}
	if produce.Schema["type"] != "object" {
		t.Errorf("produces[0].schema.type = %v, want object", produce.Schema["type"])
	}
	if len(got.Detail.Sources.Consumes) != 1 {
		t.Fatalf("consumes = %d entries, want 1", len(got.Detail.Sources.Consumes))
	}
	if got.Detail.Sources.Consumes[0].Path != "scm.repository.commit" {
		t.Errorf("consumes[0].path = %q", got.Detail.Sources.Consumes[0].Path)
	}
}

const testAccConfigToolFull = `
provider "fianu" {}

resource "fianu_tool" "scanner" {
  path = "test.tool.scanner"
  name = "Checkmarx SAST"

  detail = {
    description  = "Static analysis scanner"
    key          = "checkmarx"
    tool_type    = "sast"
    tool_version = "8.2.0"

    sources = {
      produces = [
        {
          path = "checkmarx.sast.results"
          note = "occurrence"
          integration = {
            name = "checkmarx"
            type = "tool"
          }
          schema = jsonencode({
            type = "object"
            properties = {
              findings = { type = "array" }
            }
          })
        },
      ]
      consumes = [
        {
          path = "scm.repository.commit"
          note = "origin"
        },
      ]
    }
  }
}
`

// TestAccFianuTool_Update proves an in-place change round-trips: the second
// deploy is not swallowed by the idempotency gate, and the new values land on
// the wire.
func TestAccFianuTool_Update(t *testing.T) {
	stub := newToolStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigToolMinimal,
				Check:  resource.TestCheckResourceAttr("fianu_tool.scanner", "detail.tool_version", "8.2.0"),
			},
			{
				Config: testAccConfigToolUpdated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_tool.scanner", "detail.tool_version", "9.0.1"),
					resource.TestCheckResourceAttr("fianu_tool.scanner", "detail.tool_type", "sca"),
				),
			},
		},
	})

	got := stub.captured(t)
	if got.Detail.ToolVersion != "9.0.1" {
		t.Errorf("after update, toolVersion = %q, want 9.0.1", got.Detail.ToolVersion)
	}
	if got.Detail.ToolType != "sca" {
		t.Errorf("after update, toolType = %q, want sca", got.Detail.ToolType)
	}
	if stub.deployHits.Load() < 2 {
		t.Errorf("deploy hits = %d, want at least 2 — the update never reached the server", stub.deployHits.Load())
	}
}

const testAccConfigToolUpdated = `
provider "fianu" {}

resource "fianu_tool" "scanner" {
  path = "test.tool.scanner"
  name = "Checkmarx SAST"

  detail = {
    tool_type    = "sca"
    tool_version = "9.0.1"
  }
}
`

// TestAccFianuTool_Import proves the composite ID round-trips through
// `terraform import`.
func TestAccFianuTool_Import(t *testing.T) {
	stub := newToolStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigToolMinimal},
			{
				Config:            testAccConfigToolMinimal,
				ResourceName:      "fianu_tool.scanner",
				ImportState:       true,
				ImportStateId:     "tool/test.tool.scanner",
				ImportStateVerify: true,
				// detail stays user-authored — Read hydrates the envelope only,
				// because the server applies defaults and resolves `sources`
				// integration references to UUIDs, which would drift.
				ImportStateVerifyIgnore: []string{"detail"},
			},
		},
	})
}

// TestAccFianuTool_RejectsMalformedSourceSchema pins the plan-time guard. A
// bad schema string used to be dropped silently in BuildSources, deploying a
// tool with no schema on that edge.
func TestAccFianuTool_RejectsMalformedSourceSchema(t *testing.T) {
	stub := newToolStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigToolBadSchema,
				ExpectError: regexp.MustCompile(`schema is not a JSON object`),
			},
		},
	})
}

const testAccConfigToolBadSchema = `
provider "fianu" {}

resource "fianu_tool" "scanner" {
  path = "test.tool.scanner"
  name = "Checkmarx SAST"

  detail = {
    tool_version = "8.2.0"
    sources = {
      produces = [
        {
          path   = "checkmarx.sast.results"
          schema = "{not json"
        },
      ]
    }
  }
}
`

func setEnv(t *testing.T, stub *toolStub) {
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

// toolStub fakes Console for the tool resource: deploy captures the multipart
// entity, GET echoes it back so Read doesn't drift, DELETE counts archives.
type toolStub struct {
	server       *httptest.Server
	deployHits   atomic.Int32
	archiveHits  atomic.Int32
	archivedPath atomic.Value // string
	stored       atomic.Value // *transportv1.DeployEntityFileResponse
	capturedVal  atomic.Value // *fianu_entities.Tool
	lastCaptured atomic.Value // *fianu_entities.Tool, never cleared
}

func (s *toolStub) captured(t *testing.T) *fianu_entities.Tool {
	t.Helper()
	e, _ := s.lastCaptured.Load().(*fianu_entities.Tool)
	if e == nil {
		t.Fatal("no tool captured on the deploy route")
	}
	return e
}

func newToolStub(t *testing.T) *toolStub {
	t.Helper()
	stub := &toolStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/entities/artifacts/deploy", func(w http.ResponseWriter, r *http.Request) {
		stub.deployHits.Add(1)
		req, raw := decodeDeployRequest(r)

		var e fianu_entities.Tool
		if err := json.Unmarshal(raw, &e); err == nil {
			stub.capturedVal.Store(&e)
			stub.lastCaptured.Store(&e)
		}

		// Reject anything the real endpoint would: the deploy allowlist is
		// keyed on General.entityType, so a resource sending the wrong one
		// would 400 in production but pass a stub that ignores it.
		if req.General.EntityType == nil || *req.General.EntityType != string(db_vars.EntityTypeTool) {
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
				EntityID:    "test-tool-uuid",
				Path:        entityPath,
				Name:        e.Name,
				Version:     "1",
				EntityType:  string(db_vars.EntityTypeTool),
			},
		}
		stub.stored.Store(resp)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/entities/tools/", func(w http.ResponseWriter, r *http.Request) {
		captured, _ := stub.capturedVal.Load().(*fianu_entities.Tool)
		if captured == nil {
			http.NotFound(w, r)
			return
		}
		out := *captured
		out.UUID = "test-tool-uuid"
		out.Type = db_vars.EntityTypeTool
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
		stub.capturedVal.Store((*fianu_entities.Tool)(nil))
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
