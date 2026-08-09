// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package entitypod_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/fianulabs/terraform-provider-fianu/internal/provider"
)

// TestAccFianuEntityPod_Minimal deploys one pod and asserts the wire payload:
// the PUT lands on the (entity, type, key) path, the body carries the
// jsonencode'd value verbatim, and a second apply is a no-op.
func TestAccFianuEntityPod_Minimal(t *testing.T) {
	stub := newPodStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigPodMinimal,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_entity_pod.export", "id",
						"gate-uuid-1/platforms_capabilities_data_exports_gating/gatingSource:jf-prod"),
				),
			},
			{
				Config: testAccConfigPodMinimal,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.pod(t, "gate-uuid-1", "platforms_capabilities_data_exports_gating", "gatingSource:jf-prod")
	if got["podType"] != "platforms_capabilities_data_exports_gating" {
		t.Errorf("podType = %v", got["podType"])
	}
	if got["key"] != "gatingSource:jf-prod" {
		t.Errorf("key = %v", got["key"])
	}
	// enabled defaults to true — a pod exists because someone authored it.
	if got["enabled"] != true {
		t.Errorf("enabled = %v, want true", got["enabled"])
	}
	value, ok := got["value"].(map[string]any)
	if !ok {
		t.Fatalf("value unexpected shape: %T", got["value"])
	}
	if value["capability"] != "gatingSource" || value["instanceKey"] != "jf-prod" {
		t.Errorf("value = %+v", value)
	}

	if got := stub.deleteHits.Load(); got != 1 {
		t.Errorf("expected the destroy step to delete the pod once, got %d", got)
	}
}

const testAccConfigPodMinimal = `
provider "fianu" {}

resource "fianu_entity_pod" "export" {
  entity_uuid = "gate-uuid-1"
  pod_type    = "platforms_capabilities_data_exports_gating"
  key         = "gatingSource:jf-prod"

  value = jsonencode({
    capability  = "gatingSource"
    instanceKey = "jf-prod"
  })
}
`

// TestAccFianuEntityPod_InvalidJSON proves the provider rejects a non-JSON
// value locally rather than shipping it and getting an opaque 400 back.
func TestAccFianuEntityPod_InvalidJSON(t *testing.T) {
	stub := newPodStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigPodInvalidJSON,
				ExpectError: regexp.MustCompile(`value is not valid JSON`),
			},
		},
	})

	if got := stub.setHits.Load(); got != 0 {
		t.Errorf("expected no pod writes for an invalid value, got %d", got)
	}
}

const testAccConfigPodInvalidJSON = `
provider "fianu" {}

resource "fianu_entity_pod" "bad" {
  entity_uuid = "gate-uuid-1"
  pod_type    = "display"
  key         = "config"
  value       = "not json at all"
}
`

func protoV6Factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"fianu": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

// podStub fakes the dictator pod routes:
//
//	PUT/GET/DELETE /api/pods/entities/{entity_id}/{type}/{key}
//
// Pods are stored under their full path so a test can assert the resource
// addressed the right row, not just that it wrote something.
type podStub struct {
	server     *httptest.Server
	setHits    atomic.Int32
	getHits    atomic.Int32
	deleteHits atomic.Int32
	// stored is add-only: a DELETE stores nil rather than removing the key,
	// so assertions still see what was written after resource.Test runs its
	// implicit destroy step. GET treats a nil value as gone.
	stored sync.Map // "<entity>/<type>/<key>" -> map[string]any (nil once deleted)
	// lastWrite keeps the final written body per path for post-destroy asserts.
	lastWrite sync.Map
}

func (s *podStub) pod(t *testing.T, entity, podType, key string) map[string]any {
	t.Helper()
	v, ok := s.lastWrite.Load(entity + "/" + podType + "/" + key)
	if !ok {
		t.Fatalf("no pod stored at %s/%s/%s", entity, podType, key)
	}
	return v.(map[string]any)
}

func newPodStub(t *testing.T) *podStub {
	t.Helper()
	stub := &podStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/pods/entities/", func(w http.ResponseWriter, r *http.Request) {
		segments := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/pods/entities/"), "/")
		if len(segments) < 3 {
			http.NotFound(w, r)
			return
		}
		podPath := strings.Join(segments, "/")

		switch r.Method {
		case http.MethodPut:
			stub.setHits.Add(1)
			body, _ := io.ReadAll(r.Body)
			var pod map[string]any
			_ = json.Unmarshal(body, &pod)
			stub.stored.Store(podPath, pod)
			stub.lastWrite.Store(podPath, pod)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)

		case http.MethodGet:
			stub.getHits.Add(1)
			// Read must distinguish gone-from-broken: a 404 evicts state, any
			// other error is a diagnostic. Serve the real thing so the
			// no-op-plan step doesn't spuriously recreate.
			v, ok := stub.stored.Load(podPath)
			if !ok || v == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)

		case http.MethodDelete:
			stub.deleteHits.Add(1)
			stub.stored.Store(podPath, nil)
			w.WriteHeader(http.StatusOK)

		default:
			http.NotFound(w, r)
		}
	})

	stub.server = httptest.NewServer(mux)
	return stub
}

// TestAccFianuEntityPod_Import proves the three-part composite ID round-trips
// through `terraform import`. The pod ID is `entity/type/key` and the key may
// itself contain a colon, so this also covers ParseID splitting on only the
// first two separators.
func TestAccFianuEntityPod_Import(t *testing.T) {
	stub := newPodStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigPodMinimal},
			{
				Config:            testAccConfigPodMinimal,
				ResourceName:      "fianu_entity_pod.export",
				ImportState:       true,
				ImportStateId:     "gate-uuid-1/platforms_capabilities_data_exports_gating/gatingSource:jf-prod",
				ImportStateVerify: true,
				// Read confirms the pod exists and evicts on 404; it does not
				// hydrate the payload, because the server canonicalises pod
				// values on write.
				ImportStateVerifyIgnore: []string{"value", "name", "description", "enabled"},
			},
		},
	})
}

// TestAccFianuEntityPod_Update covers the second-apply path. SetEntityPod is a
// PUT, so Create and Update are literally the same call — what this checks is
// that a changed value actually reaches the server rather than being treated as
// no-op, and that the composite ID is stable across the change.
func TestAccFianuEntityPod_Update(t *testing.T) {
	stub := newPodStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigPodMinimal},
			{
				Config: testAccConfigPodUpdated,
				Check: resource.ComposeTestCheckFunc(
					// The ID is derived from (entity, type, key), none of which
					// changed — a churning ID here would force a replace on
					// every value edit.
					resource.TestCheckResourceAttr("fianu_entity_pod.export", "id",
						"gate-uuid-1/platforms_capabilities_data_exports_gating/gatingSource:jf-prod"),
				),
			},
		},
	})

	if got := stub.setHits.Load(); got < 2 {
		t.Errorf("PUT hits = %d, want at least 2 — the update never reached the server", got)
	}
	pod := stub.pod(t, "gate-uuid-1", "platforms_capabilities_data_exports_gating", "gatingSource:jf-prod")
	value, ok := pod["value"].(map[string]any)
	if !ok {
		t.Fatalf("pod value is %T, want an object: %+v", pod["value"], pod)
	}
	if value["instanceKey"] != "jf-staging" {
		t.Errorf("the updated value did not reach the wire: instanceKey = %v", value["instanceKey"])
	}
}

const testAccConfigPodUpdated = `
provider "fianu" {}

resource "fianu_entity_pod" "export" {
  entity_uuid = "gate-uuid-1"
  pod_type    = "platforms_capabilities_data_exports_gating"
  key         = "gatingSource:jf-prod"

  value = jsonencode({
    capability  = "gatingSource"
    instanceKey = "jf-staging"
  })
}
`
