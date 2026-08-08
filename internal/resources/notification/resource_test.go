// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package notification_test

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

	entity_pods "github.com/fianulabs/core/v2/external/db/types/fianu/entities/pods"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	pkgvars "github.com/fianulabs/core/v2/external/pkg/variables"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/fianulabs/terraform-provider-fianu/internal/provider"
)

// TestAccFianuNotification_Minimal is the smallest useful config: a bucket, an
// entity, on. Proves the key defaults to the platform's "config" convention
// and that a second apply is a no-op.
func TestAccFianuNotification_Minimal(t *testing.T) {
	stub := newPodStub(t)
	defer stub.server.Close()

	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigNotificationMinimal,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_notification.blocking", "key", db_vars.NotificationConfigKey),
					resource.TestCheckResourceAttr("fianu_notification.blocking", "id",
						"gate-uuid-1/notification_blocking/"+db_vars.NotificationConfigKey),
				),
			},
			{
				Config: testAccConfigNotificationMinimal,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	cfg := stub.config(t, "gate-uuid-1", "notification_blocking", db_vars.NotificationConfigKey)
	if !cfg.Enabled {
		t.Error("enabled should be true on the wire")
	}
}

const testAccConfigNotificationMinimal = `
provider "fianu" {}

resource "fianu_notification" "blocking" {
  entity_uuid = "gate-uuid-1"
  type        = "notification_blocking"
  enabled     = true
}
`

// TestAccFianuNotification_FullSpec exercises every knob and asserts the
// deserialized NotificationConfig on the wire — including that `rules` came
// through as a PolicyAssetGroup with the CEL pre-parsed into ExprSource, which
// is what the server's validator compiles.
func TestAccFianuNotification_FullSpec(t *testing.T) {
	stub := newPodStub(t)
	defer stub.server.Close()

	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigNotificationFull},
			{
				Config: testAccConfigNotificationFull,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	cfg := stub.config(t, "control-uuid-1", "notification_attestation_failure", db_vars.NotificationConfigKey)

	if !cfg.Enabled {
		t.Error("enabled = false, want true")
	}
	if cfg.Urgency != 4 {
		t.Errorf("urgency = %d, want 4", cfg.Urgency)
	}
	if cfg.Mode != pkgvars.FireModeTransition {
		t.Errorf("mode = %q, want transition", cfg.Mode)
	}
	if want := []pkgvars.Recipient{pkgvars.RecipientControlOwner, pkgvars.RecipientAssetOwner}; !equalRecipients(cfg.Recipients, want) {
		t.Errorf("recipients = %v, want %v", cfg.Recipients, want)
	}
	if want := []pkgvars.Channel{pkgvars.ChannelEmail, pkgvars.ChannelSlack}; !equalChannels(cfg.Channels, want) {
		t.Errorf("channels = %v, want %v", cfg.Channels, want)
	}

	if cfg.Rules == nil {
		t.Fatal("rules is nil — the asset matcher never reached the wire")
	}
	if cfg.Rules.Asset == nil || string(cfg.Rules.Asset.Type) != "repository" {
		t.Errorf("rules.asset = %+v, want type=repository", cfg.Rules.Asset)
	}
	if len(cfg.Rules.Expressions) != 1 {
		t.Fatalf("expected 1 rules expression, got %d", len(cfg.Rules.Expressions))
	}
	// The provider pre-parses CEL into the canonical form the server compiles;
	// shipping only the raw string would fail server-side validation.
	if cfg.Rules.Expressions[0].ExprSource == "" {
		t.Errorf("rules.expressions[0].exprSource is empty; raw=%+v", cfg.Rules.Expressions[0])
	}

	if cfg.Params.WindowDays == nil || *cfg.Params.WindowDays != 30 {
		t.Errorf("params.windowDays = %v, want 30", cfg.Params.WindowDays)
	}
	if cfg.Params.MinAssets == nil || *cfg.Params.MinAssets != 5 {
		t.Errorf("params.minAssets = %v, want 5", cfg.Params.MinAssets)
	}
	// Unset params must stay nil so the engine applies its own defaults
	// instead of reading a provider-supplied zero.
	if cfg.Params.LeadWindowDays != nil {
		t.Errorf("params.leadWindowDays = %v, want nil (unset)", *cfg.Params.LeadWindowDays)
	}

	if len(cfg.Filters) != 2 {
		t.Fatalf("expected 2 filters, got %d: %+v", len(cfg.Filters), cfg.Filters)
	}
	if f := cfg.Filters["gate_blocking"]; !f.Enabled || f.Urgency != 5 {
		t.Errorf("filters[gate_blocking] = %+v, want enabled urgency 5", f)
	}
	if f := cfg.Filters["on_any_commit"]; f.Enabled {
		t.Errorf("filters[on_any_commit] = %+v, want disabled", f)
	}
}

const testAccConfigNotificationFull = `
provider "fianu" {}

resource "fianu_notification" "sast_failures" {
  entity_uuid = "control-uuid-1"
  type        = "notification_attestation_failure"

  enabled    = true
  urgency    = 4
  mode       = "transition"
  recipients = ["control_owner", "asset_owner"]
  channels   = ["email", "slack"]

  rules = {
    asset = { type = "repository" }
    expressions = [
      { expression = "asset.scm.repository startsWith 'prod-'" },
    ]
  }

  params = {
    window_days = 30
    min_assets  = 5
  }

  filters = {
    gate_blocking = { enabled = true, urgency = 5 }
    on_any_commit = { enabled = false }
  }
}
`

// TestAccFianuNotification_UrgencyOutOfRange proves the schema validator
// rejects an out-of-band urgency at plan time — before any HTTP call.
func TestAccFianuNotification_UrgencyOutOfRange(t *testing.T) {
	stub := newPodStub(t)
	defer stub.server.Close()

	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigNotificationBadUrgency,
				ExpectError: regexp.MustCompile(`Invalid Attribute Value`),
			},
		},
	})

	if got := stub.setHits.Load(); got != 0 {
		t.Errorf("expected no writes for an invalid urgency, got %d", got)
	}
}

const testAccConfigNotificationBadUrgency = `
provider "fianu" {}

resource "fianu_notification" "bad" {
  entity_uuid = "control-uuid-1"
  type        = "notification_recovery"
  enabled     = true
  urgency     = 9
}
`

// TestAccFianuNotification_UnknownType proves `type` is validated against the
// SDK's registry rather than accepted and 400'd server-side.
func TestAccFianuNotification_UnknownType(t *testing.T) {
	stub := newPodStub(t)
	defer stub.server.Close()

	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigNotificationBadType,
				ExpectError: regexp.MustCompile(`Invalid Attribute Value Match`),
			},
		},
	})
}

const testAccConfigNotificationBadType = `
provider "fianu" {}

resource "fianu_notification" "bad" {
  entity_uuid = "control-uuid-1"
  type        = "notification_does_not_exist"
  enabled     = true
}
`

func setEnv(t *testing.T, stub *podStub) {
	t.Helper()
	t.Setenv("TF_ACC", "1")
	t.Setenv("FIANU_HOST", stub.server.URL)
	t.Setenv("FIANU_TOKEN", "test-bearer")
}

func equalRecipients(got, want []pkgvars.Recipient) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalChannels(got, want []pkgvars.Channel) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func protoV6Factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"fianu": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

// podStub fakes the dictator pod routes the notification resource rides on:
//
//	PUT/GET/DELETE /api/pods/entities/{entity_id}/{type}/{key}
//
// lastWrite is add-only so assertions still see the payload after
// resource.Test runs its implicit destroy step.
type podStub struct {
	server     *httptest.Server
	setHits    atomic.Int32
	deleteHits atomic.Int32
	stored     sync.Map // path -> json.RawMessage of the pod (nil once deleted)
	lastWrite  sync.Map // path -> json.RawMessage of the pod
}

// config decodes the NotificationConfig out of the last pod written at the
// given address — asserting on the typed struct, not on loose map keys, so a
// field rename in core fails the test at compile time.
func (s *podStub) config(t *testing.T, entity, podType, key string) entity_pods.NotificationConfig {
	t.Helper()
	v, ok := s.lastWrite.Load(entity + "/" + podType + "/" + key)
	if !ok {
		t.Fatalf("no pod written at %s/%s/%s", entity, podType, key)
	}
	var pod struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(v.([]byte), &pod); err != nil {
		t.Fatalf("decode pod: %v", err)
	}
	var cfg entity_pods.NotificationConfig
	if err := json.Unmarshal(pod.Value, &cfg); err != nil {
		t.Fatalf("decode notification config: %v", err)
	}
	return cfg
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
			stub.stored.Store(podPath, body)
			stub.lastWrite.Store(podPath, body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)

		case http.MethodGet:
			v, ok := stub.stored.Load(podPath)
			if !ok || v == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(v.([]byte))

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
