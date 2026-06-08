// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package index_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	core_indexes "github.com/fianulabs/core/v2/external/db/indexes"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/fianulabs/terraform-provider-fianu/internal/provider"
)

// TestAccFianuIndex_Minimal exercises the bare envelope + required Detail
// fields (asset_type + a single expression). Asserts the captured wire
// payload and the idempotency contract end-to-end.
func TestAccFianuIndex_Minimal(t *testing.T) {
	stub := newIndexStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigMinimalIndex,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_index.example", "path", "test.index.basic"),
					resource.TestCheckResourceAttr("fianu_index.example", "name", "Basic Test Index"),
					resource.TestCheckResourceAttr("fianu_index.example", "detail.asset_type", "repository"),
					resource.TestCheckResourceAttr("fianu_index.example", "id", "index/test.index.basic"),
				),
			},
			{
				Config: testAccConfigMinimalIndex,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	captured, _ := stub.capturedEntity.Load().(*fianu_entities.Index)
	if captured == nil {
		t.Fatalf("expected captured Index, got nil")
	}
	if captured.Detail.AssetTypePath != "repository" {
		t.Errorf("detail.assetTypePath = %q, want %q", captured.Detail.AssetTypePath, "repository")
	}
	if len(captured.Detail.Expressions) != 1 {
		t.Fatalf("expected 1 expression, got %d", len(captured.Detail.Expressions))
	}
	if captured.Detail.Expressions[0].Source == "" {
		t.Error("expressions[0].source is empty")
	}
}

const testAccConfigMinimalIndex = `
provider "fianu" {}

resource "fianu_index" "example" {
  path = "test.index.basic"
  name = "Basic Test Index"

  detail = {
    asset_type = "repository"
    expressions = [
      { source = "asset.scm.repository startsWith 'prod-'" },
    ]
  }
}
`

// TestAccFianuIndex_FullSpec exercises every Detail field: description,
// dependent_asset_types, explicit combine_with + kind, multiple expressions.
func TestAccFianuIndex_FullSpec(t *testing.T) {
	stub := newIndexStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigFullSpecIndex},
			{
				Config: testAccConfigFullSpecIndex,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	captured, _ := stub.capturedEntity.Load().(*fianu_entities.Index)
	if captured == nil {
		t.Fatalf("expected captured Index, got nil")
	}
	if captured.Detail.Description != "All production repos under SOX scope" {
		t.Errorf("description = %q", captured.Detail.Description)
	}
	if string(captured.Detail.CombineWith) != "OR" {
		t.Errorf("combineWith = %q, want OR", captured.Detail.CombineWith)
	}
	if string(captured.Detail.Kind) != "write-ahead" {
		t.Errorf("kind = %q, want write-ahead", captured.Detail.Kind)
	}
	if len(captured.Detail.DependentAssetTypes) != 1 || captured.Detail.DependentAssetTypes[0] != "application" {
		t.Errorf("dependentAssetTypes = %v, want [application]", captured.Detail.DependentAssetTypes)
	}
	if len(captured.Detail.Expressions) != 2 {
		t.Fatalf("expected 2 expressions, got %d", len(captured.Detail.Expressions))
	}
	for i, expr := range captured.Detail.Expressions {
		if expr.Seq != i+1 {
			t.Errorf("expressions[%d].seq = %d, want %d", i, expr.Seq, i+1)
		}
		if expr.Source == "" {
			t.Errorf("expressions[%d].source is empty", i)
		}
		if expr.DisplayText == "" {
			t.Errorf("expressions[%d].displayText is empty (provider should preserve raw CEL)", i)
		}
	}
}

const testAccConfigFullSpecIndex = `
provider "fianu" {}

resource "fianu_index" "full" {
  path = "test.index.full"
  name = "Full Spec Index"

  detail = {
    description           = "All production repos under SOX scope"
    asset_type            = "repository"
    dependent_asset_types = ["application"]
    combine_with          = "OR"
    kind                  = "write-ahead"
    expressions = [
      { source = "asset.scm.repository startsWith 'prod-'" },
      { source = "asset.scm.repository endsWith '-sox'" },
    ]
  }
}
`

// TestAccFianuIndex_Update covers the PATCH wire path — _Minimal and
// _FullSpec ship two identical Config Steps so resource.Test never calls
// the provider's Update method between them. Changing a non-RequiresReplace
// field (here: name and one expression) forces an Update and confirms the
// SDK's UpdateIndex (PATCH /api/entities/indexes/{key}) is wired correctly
// end-to-end.
func TestAccFianuIndex_Update(t *testing.T) {
	stub := newIndexStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	const cfgInitial = `
provider "fianu" {}
resource "fianu_index" "upd" {
  path = "test.index.update"
  name = "Original Name"
  detail = {
    asset_type = "repository"
    expressions = [
      { source = "asset.scm.repository startsWith 'old-'" },
    ]
  }
}
`
	const cfgUpdated = `
provider "fianu" {}
resource "fianu_index" "upd" {
  path = "test.index.update"
  name = "Updated Name"
  detail = {
    asset_type = "repository"
    expressions = [
      { source = "asset.scm.repository startsWith 'new-'" },
    ]
  }
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: cfgInitial},
			{Config: cfgUpdated},
		},
	})

	if got := stub.createHits.Load(); got != 1 {
		t.Errorf("expected 1 create hit, got %d (Update must not POST /indexes)", got)
	}
	if got := stub.updateHits.Load(); got < 1 {
		t.Errorf("expected at least 1 update hit, got %d (Update should PATCH /indexes/:key)", got)
	}

	captured, _ := stub.capturedEntity.Load().(*fianu_entities.Index)
	if captured == nil {
		t.Fatal("expected captured Index, got nil")
	}
	if captured.Name != "Updated Name" {
		t.Errorf("name after update = %q, want %q", captured.Name, "Updated Name")
	}
	if len(captured.Detail.Expressions) != 1 {
		t.Fatalf("expected 1 expression after update, got %d", len(captured.Detail.Expressions))
	}
	if got := captured.Detail.Expressions[0].DisplayText; got != "asset.scm.repository startsWith 'new-'" {
		t.Errorf("expression after update = %q", got)
	}
}

// TestAccFianuIndex_Import verifies that `terraform import` populates state
// from the server and a follow-up plan is a no-op. Hydration is envelope-
// only, so import only needs to resolve path → entity envelope; Detail
// fields stay user-authored.
func TestAccFianuIndex_Import(t *testing.T) {
	stub := newIndexStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	const cfg = `
provider "fianu" {}
resource "fianu_index" "imp" {
  path = "test.index.import"
  name = "Importable Index"
  detail = {
    asset_type = "repository"
    expressions = [
      { source = "asset.scm.repository startsWith 'prod-'" },
    ]
  }
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				ResourceName:      "fianu_index.imp",
				ImportState:       true,
				ImportStateId:     "index/test.index.import",
				ImportStateVerify: true,
				// Other detail fields (description/combine_with/kind/
				// dependent_asset_types/expressions) aren't hydrated — they
				// stay user-authored to avoid drift against server-side
				// canonicalisation. detail.asset_type IS hydrated because
				// it's RequiresReplace; without that, the post-import plan
				// would force destroy+create when the user adds matching
				// HCL.
				ImportStateVerifyIgnore: []string{
					"detail.description",
					"detail.combine_with",
					"detail.kind",
					"detail.dependent_asset_types",
					"detail.expressions",
				},
			},
		},
	})
}

// TestAccFianuIndex_ExpressionPreParse regresses the index analogue of the
// policy CEL pre-parse contract: IndexExpressionSource.Source must carry the
// canonical parsed form (with $ prefix + .(type) casts); DisplayText carries
// the user's raw text. There's no Expr fallback field on
// IndexExpressionSource, so a successful parse is the only path that produces
// a server-acceptable payload.
func TestAccFianuIndex_ExpressionPreParse(t *testing.T) {
	stub := newIndexStub(t)
	defer stub.server.Close()

	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")

	cfg := `
provider "fianu" {}
resource "fianu_index" "preparse" {
  path = "test.index.preparse"
  name = "Pre-Parse Test"
  detail = {
    asset_type = "repository"
    expressions = [
      { source = "asset.scm.repository startsWith 'prod-'" },
    ]
  }
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps:                    []resource.TestStep{{Config: cfg}},
	})

	captured, _ := stub.capturedEntity.Load().(*fianu_entities.Index)
	if captured == nil {
		t.Fatal("no entity captured")
	}
	if len(captured.Detail.Expressions) != 1 {
		t.Fatalf("expected 1 expression, got %d", len(captured.Detail.Expressions))
	}
	raw := "asset.scm.repository startsWith 'prod-'"
	expr := captured.Detail.Expressions[0]
	if expr.DisplayText != raw {
		t.Errorf("displayText = %q, want %q", expr.DisplayText, raw)
	}
	if expr.Source == "" {
		t.Error("source is empty — provider should have populated it via cel.ParseExpression")
	}
	if expr.Source == raw {
		t.Errorf("source = %q, expected the *parsed* canonical form (with $ prefix), got the raw form", expr.Source)
	}
}

// protoV6Factories returns the protocol-v6 factories the testing framework
// requires.
func protoV6Factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"fianu": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

// indexStub mounts the index REST shape — Create/Update/Read/Delete use
// dedicated routes (NOT the generic deploy_entity_file flow that
// control/gate/policy share).
//
//	POST   /api/entities/indexes            — Create
//	GET    /api/entities/indexes/{key}      — Read (echoes captured entity)
//	PATCH  /api/entities/indexes/{key}      — Update
//	DELETE /api/entities/indexes/{key}      — Archive
//
// Each successful Create/Update/Read returns an `IndexWithComputeState`
// (canonical wire shape: embedded `entities.Index` + ComputeState wrapper).
type indexStub struct {
	server         *httptest.Server
	createHits     atomic.Int32
	fetchHits      atomic.Int32
	updateHits     atomic.Int32
	archiveHits    atomic.Int32
	capturedEntity atomic.Value // *fianu_entities.Index
}

func newIndexStub(t *testing.T) *indexStub {
	t.Helper()
	stub := &indexStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/entities/indexes", func(w http.ResponseWriter, r *http.Request) {
		// /indexes (no trailing key) — POST only (Create).
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		stub.createHits.Add(1)
		entity := decodeIndexBody(r)
		if entity == nil {
			http.Error(w, "could not decode index body", http.StatusBadRequest)
			return
		}
		stamp(entity)
		stub.capturedEntity.Store(entity)
		writeIndexResponse(w, entity)
	})

	mux.HandleFunc("/api/entities/indexes/", func(w http.ResponseWriter, r *http.Request) {
		// /indexes/{key} — GET (Read), PATCH (Update), DELETE (Archive).
		switch r.Method {
		case http.MethodGet:
			stub.fetchHits.Add(1)
			captured, _ := stub.capturedEntity.Load().(*fianu_entities.Index)
			if captured == nil {
				http.NotFound(w, r)
				return
			}
			writeIndexResponse(w, captured)
		case http.MethodPatch:
			stub.updateHits.Add(1)
			entity := decodeIndexBody(r)
			if entity == nil {
				http.Error(w, "could not decode index body", http.StatusBadRequest)
				return
			}
			// Preserve identity fields the user can't change cross-update so
			// the echoed payload matches what the provider expects on Read.
			if prior, _ := stub.capturedEntity.Load().(*fianu_entities.Index); prior != nil {
				entity.UUID = prior.UUID
				entity.Version.UUID = prior.Version.UUID
			}
			stamp(entity)
			stub.capturedEntity.Store(entity)
			writeIndexResponse(w, entity)
		case http.MethodDelete:
			stub.archiveHits.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	stub.server = httptest.NewServer(mux)
	return stub
}

// decodeIndexBody decodes a JSON-marshalled Index out of the request body.
// CreateIndex/UpdateIndex use plain application/json (NOT multipart), so the
// decode is one step.
func decodeIndexBody(r *http.Request) *fianu_entities.Index {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return nil
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	var e fianu_entities.Index
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil
	}
	return &e
}

// stamp fills in the server-side fields a real Console would populate before
// echoing the entity back: UUID, version sub-fields, status/state, asset-type
// UUID, and entity Type. These are computed/asserted by the provider's
// envelope hydration, so the stub has to return them for Read to roundtrip
// without surfacing drift on the next plan.
func stamp(e *fianu_entities.Index) {
	e.Type = db_vars.EntityTypeIndex
	if e.UUID == "" {
		e.UUID = "test-index-uuid"
	}
	if e.Version.UUID == "" {
		e.Version.UUID = "version-uuid"
	}
	if e.Version.Semantic == "" {
		e.Version.Semantic = "1"
	}
	if e.Version.Status == "" {
		e.Version.Status = db_vars.EntityStatusActive
	}
	if e.Version.State == "" {
		e.Version.State = db_vars.EntityStatePublished
	}
	if e.Detail.AssetType == "" {
		// Real server resolves AssetTypePath -> UUID; the stub stamps a
		// stable UUID here so reads round-trip.
		e.Detail.AssetType = "11111111-1111-1111-1111-111111111111"
	}
}

func writeIndexResponse(w http.ResponseWriter, e *fianu_entities.Index) {
	wrapper := core_indexes.IndexWithComputeState{Index: *e}
	wrapper.ComputeState.MemberCount = 0
	wrapper.ComputeState.RecomputeStatus = "ready"
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(wrapper)
}
