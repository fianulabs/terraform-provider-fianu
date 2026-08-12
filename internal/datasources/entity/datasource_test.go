// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package entity_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/fianulabs/terraform-provider-fianu/internal/provider"
)

func protoV6Factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"fianu": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

func setEnv(t *testing.T, stub *readStub) {
	t.Helper()
	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")
}

// readStub answers every canonical single-entity GET with the same envelope
// shape, recording which URL path was asked for.
//
// A catch-all rather than one handler per route on purpose: the routes
// themselves are the SDK's business and are already covered by each resource's
// own stub. What is unproven here is kind.go — thirteen hand-written closures,
// each calling a different SDK method with a different signature and unpacking
// a different return type. The catch-all isolates that.
type readStub struct {
	server *httptest.Server

	mu    sync.Mutex
	paths []string

	// notFound makes every read 404, for the missing-entity case.
	notFound bool
}

func newReadStub(t *testing.T) *readStub {
	t.Helper()
	stub := &readStub{}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.paths = append(stub.paths, r.URL.Path)
		notFound := stub.notFound
		stub.mu.Unlock()

		if notFound {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}

		// The entity key is the last path segment for every canonical read.
		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		key := segments[len(segments)-1]

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uuid": "uuid-" + key,
			"path": key,
			"name": "Name " + key,
			"version": map[string]any{
				"semantic":  "3",
				"uuid":      "version-uuid-" + key,
				"status":    "active",
				"state":     "published",
				"timestamp": "2026-01-01T00:00:00Z",
			},
		})
	})

	stub.server = httptest.NewServer(handler)
	return stub
}

func (s *readStub) requestedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

// dataSourceCases is every data source the provider registers, paired with the
// entity_type prefix its composite `id` must carry.
//
// The prefixes are restated here rather than read from the (unexported) kinds
// table so the test fails when kind.go changes rather than agreeing with it.
// `report_template` is the one that matters: the HCL type name and the wire
// entity type genuinely differ, and getting it wrong yields an `id` that
// base.ParseID rejects.
var dataSourceCases = []struct {
	typeName   string
	entityType string
}{
	{"fianu_collection", "collection"},
	{"fianu_control", "control"},
	{"fianu_environment", "environment"},
	{"fianu_form", "form"},
	{"fianu_gate", "gate"},
	{"fianu_index", "index"},
	{"fianu_instance", "instance"},
	{"fianu_platform", "platform"},
	{"fianu_policy", "policy"},
	{"fianu_policy_exception", "policy_exception"},
	{"fianu_report_template", "template"},
	{"fianu_target", "target"},
	{"fianu_tool", "tool"},
}

// TestAccDataSources_EveryKindResolves is the load-bearing test. Each closure
// in kind.go picks a different SDK method and reaches through a different
// number of embedded structs to find the envelope — `e` for the type aliases
// (environment, report template), `&e.StandardEntity` for the embedded shapes,
// and two levels of promotion for target. A closure that compiles but reads
// the wrong embed returns a zero envelope: empty uuid, empty name, and an `id`
// of `<type>/`. Nothing errors.
func TestAccDataSources_EveryKindResolves(t *testing.T) {
	for _, tc := range dataSourceCases {
		t.Run(tc.typeName, func(t *testing.T) {
			stub := newReadStub(t)
			defer stub.server.Close()
			setEnv(t, stub)

			key := "test.entity.key"
			addr := fmt.Sprintf("data.%s.subject", tc.typeName)

			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: protoV6Factories(),
				Steps: []resource.TestStep{
					{
						Config: fmt.Sprintf(`
provider "fianu" {}

data %q "subject" {
  path = %q
}
`, tc.typeName, key),
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttr(addr, "id", tc.entityType+"/"+key),
							resource.TestCheckResourceAttr(addr, "uuid", "uuid-"+key),
							resource.TestCheckResourceAttr(addr, "path", key),
							resource.TestCheckResourceAttr(addr, "name", "Name "+key),
							resource.TestCheckResourceAttr(addr, "version.semantic", "3"),
							resource.TestCheckResourceAttr(addr, "version.status", "active"),
							resource.TestCheckResourceAttr(addr, "version.state", "published"),
						),
					},
				},
			})

			if len(stub.requestedPaths()) == 0 {
				t.Fatal("no request reached the stub — the data source did not perform a read")
			}
		})
	}
}

// TestAccDataSource_NotFoundIsAnError pins the difference from the resource
// Read path. There, a 404 evicts state so the next apply recreates the entity.
// Here it must fail the plan: the configuration references something that does
// not exist, and returning a zero-valued envelope would feed an empty uuid to
// whatever depends on it — a `fianu_instance` deployed with
// `platform_uuid = ""` rather than an error.
func TestAccDataSource_NotFoundIsAnError(t *testing.T) {
	stub := newReadStub(t)
	defer stub.server.Close()
	stub.notFound = true
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: `
provider "fianu" {}

data "fianu_platform" "missing" {
  path = "test.platform.nope"
}
`,
				ExpectError: regexp.MustCompile(`platform "test\.platform\.nope" not found`),
			},
		},
	})
}

// TestAccDataSource_FeedsResourceReference is the whole point of the feature:
// a uuid resolved by lookup, not hardcoded, flowing into an attribute that
// needs it.
func TestAccDataSource_FeedsResourceReference(t *testing.T) {
	stub := newReadStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: `
provider "fianu" {}

data "fianu_platform" "jira" {
  path = "f.platform.jira"
}

output "platform_uuid" {
  value = data.fianu_platform.jira.uuid
}
`,
				Check: resource.TestCheckOutput("platform_uuid", "uuid-f.platform.jira"),
			},
		},
	})
}
