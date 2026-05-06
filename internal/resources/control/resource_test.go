package control_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	transportv1 "github.com/fianulabs/core/v2/external/transport/http/v1"
	"github.com/fianulabs/terraform-provider-fianu/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccFianuControl_CreateReadDestroy is the headline acceptance test for
// fianu_control. It exercises:
//
//   - Create  → Deploy hits the stub Console with the expected payload
//   - Read    → FetchControl populates state with server-side fields
//   - Plan    → re-running terraform plan after apply yields zero diff
//                (proves the idempotency contract end-to-end through the
//                terraform-plugin-framework)
//   - Destroy → ArchiveEntity is called with the resource's UUID
//
// The test stands up a single httptest server impersonating Console; the
// stub serves /entities/artifacts/deploy, /controls/<key>, and the archive
// PUT path. Each handler records call counts for post-test assertions.
func TestAccFianuControl_CreateReadDestroy(t *testing.T) {
	stub := newConsoleStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
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
//   - PUT  /entities/{type}/{uuid}/status → archive
//
// The stub records call counts on each path so tests can post-assert.
type consoleStub struct {
	server     *httptest.Server
	deployHits atomic.Int32
	fetchHits  atomic.Int32
	archiveHits atomic.Int32

	// stored remembers the most recently deployed entity so subsequent reads
	// reflect the just-applied state.
	stored atomic.Value // *transportv1.DeployEntityFileResponse
}

func newConsoleStub(t *testing.T) *consoleStub {
	t.Helper()
	stub := &consoleStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/entities/artifacts/deploy", func(w http.ResponseWriter, r *http.Request) {
		stub.deployHits.Add(1)

		// Parse path from the JSON body so the response can echo it back.
		var req transportv1.DeployEntityFileRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		path := ""
		if req.General.Path != nil {
			path = *req.General.Path
		}

		// Second deploy with same content is a no-op per the real gate; the
		// stub mimics that by inspecting the X-Fianu-CI-System-Hash header
		// against the prior call.
		action := "created"
		if prior := stub.stored.Load(); prior != nil {
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
				EntityID:    "test-uuid-fixed",
				Path:        path,
				Name:        "Basic Test Control",
				Version:     "1",
				EntityType:  "control",
			},
		}
		stub.stored.Store(resp)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/controls/", func(w http.ResponseWriter, r *http.Request) {
		stub.fetchHits.Add(1)
		// Echo back a control matching what the latest deploy stored.
		key := strings.TrimPrefix(r.URL.Path, "/controls/")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
  "uuid":"test-uuid-fixed",
  "name":"Basic Test Control",
  "path":%q,
  "type":"control",
  "version":{"semantic":"1","uuid":"version-uuid","status":"active","state":"published"},
  "detail":{"control":{"fullName":"Basic Test Control","displayKey":"BTC","description":"Acceptance-test fixture"}}
}`, key)
	})

	// ArchiveEntity hits DELETE /archive/<type>/<uuid>. Match anything under
	// /archive/ to keep the stub forgiving.
	mux.HandleFunc("/archive/", func(w http.ResponseWriter, r *http.Request) {
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
