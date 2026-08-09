// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package platform_test

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

// TestAccFianuPlatform_Minimal covers identity only.
func TestAccFianuPlatform_Minimal(t *testing.T) {
	stub := newPlatformStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigPlatformMinimal,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_platform.github", "id", "platform/test.platform.github"),
					resource.TestCheckResourceAttr("fianu_platform.github", "uuid", "test-platform-uuid"),
					// From the refetch, not the deploy metadata.
					resource.TestCheckResourceAttr("fianu_platform.github", "version.status", "active"),
				),
			},
			{
				Config: testAccConfigPlatformMinimal,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)
	if got.Type != db_vars.EntityTypePlatform {
		t.Errorf("entity type = %q, want platform", got.Type)
	}
	if got.Path != "test.platform.github" {
		t.Errorf("path = %q", got.Path)
	}
}

const testAccConfigPlatformMinimal = `
provider "fianu" {}

resource "fianu_platform" "github" {
  path = "test.platform.github"
  name = "GitHub"

  detail = {
    description = "GitHub SCM platform"
  }
}
`

// TestAccFianuPlatform_FullSpec exercises all five optional sub-sections and
// asserts each lands on the wire with its enums and JSON columns intact.
func TestAccFianuPlatform_FullSpec(t *testing.T) {
	stub := newPlatformStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigPlatformFull},
			{
				Config: testAccConfigPlatformFull,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})

	got := stub.captured(t)

	if got.Detail.PlatformType.UUID != "platform-type-uuid" {
		t.Errorf("platformType.uuid = %q", got.Detail.PlatformType.UUID)
	}
	if got.Detail.Features["webhooks"] != "true" {
		t.Errorf("features[webhooks] = %q, want true", got.Detail.Features["webhooks"])
	}

	if got.Detail.EndpointDefaults == nil {
		t.Fatal("endpointDefaults was dropped")
	}
	if got.Detail.EndpointDefaults.BaseURL != "https://api.github.com" {
		t.Errorf("endpointDefaults.baseUrl = %q", got.Detail.EndpointDefaults.BaseURL)
	}
	if got.Detail.EndpointDefaults.DefaultHeaders["Accept"] != "application/vnd.github+json" {
		t.Errorf("endpointDefaults.defaultHeaders = %v — the jsonencode string did not reach the wire", got.Detail.EndpointDefaults.DefaultHeaders)
	}

	if len(got.Detail.HealthChecks) != 1 {
		t.Fatalf("healthChecks = %d, want 1", len(got.Detail.HealthChecks))
	}
	hc := got.Detail.HealthChecks[0]
	if hc.CheckKey != "api_reachable" {
		t.Errorf("healthChecks[0].checkKey = %q", hc.CheckKey)
	}
	if hc.Severity != db_vars.PlatformHealthSeverityCritical {
		t.Errorf("healthChecks[0].severity = %q, want critical", hc.Severity)
	}
	if !hc.Enabled {
		t.Error("healthChecks[0].enabled = false, want true")
	}
	if hc.IntervalSeconds != 60 || hc.TimeoutMs != 5000 || hc.RetryMax != 3 || hc.RetryBackoffMs != 250 {
		t.Errorf("healthChecks[0] numeric fields = interval %d timeout %d retries %d backoff %d",
			hc.IntervalSeconds, hc.TimeoutMs, hc.RetryMax, hc.RetryBackoffMs)
	}
	if hc.SuccessPredicate == nil {
		t.Error("healthChecks[0].successPredicate was dropped")
	}

	if got.Detail.CredentialPolicy == nil {
		t.Fatal("credentialPolicy was dropped")
	}
	if got.Detail.CredentialPolicy.Rotation != db_vars.RotationModeProactive {
		t.Errorf("credentialPolicy.rotation = %q, want proactive", got.Detail.CredentialPolicy.Rotation)
	}
	if got.Detail.CredentialPolicy.GracePeriodSeconds != 300 {
		t.Errorf("credentialPolicy.gracePeriodSeconds = %d, want 300", got.Detail.CredentialPolicy.GracePeriodSeconds)
	}

	if len(got.Detail.ErrorMappings) != 2 {
		t.Fatalf("errorMappings = %d, want 2", len(got.Detail.ErrorMappings))
	}
	rateLimit := got.Detail.ErrorMappings[0]
	if rateLimit.HTTPStatus == nil || *rateLimit.HTTPStatus != 429 {
		t.Errorf("errorMappings[0].httpStatus = %v, want 429", rateLimit.HTTPStatus)
	}
	if rateLimit.Classification != db_vars.PlatformErrorClassRateLimit {
		t.Errorf("errorMappings[0].classification = %q", rateLimit.Classification)
	}
	if rateLimit.Action != db_vars.PlatformErrorActionBackoff {
		t.Errorf("errorMappings[0].action = %q, want backoff", rateLimit.Action)
	}
	// A mapping with no http_status must send null, not 0: 0 is not a status
	// code, and the field is a pointer on the wire precisely to say "absent".
	if got.Detail.ErrorMappings[1].HTTPStatus != nil {
		t.Errorf("errorMappings[1].httpStatus = %v, want nil for a non-HTTP failure", *got.Detail.ErrorMappings[1].HTTPStatus)
	}

	if got.Detail.AuditPolicy == nil {
		t.Fatal("auditPolicy was dropped")
	}
	if got.Detail.AuditPolicy.Level != db_vars.PlatformLogLevelInfo {
		t.Errorf("auditPolicy.level = %q, want info", got.Detail.AuditPolicy.Level)
	}
	if got.Detail.AuditPolicy.PIIHandling != db_vars.PlatformPIIModeRedact {
		t.Errorf("auditPolicy.piiHandling = %q, want redact", got.Detail.AuditPolicy.PIIHandling)
	}
	if got.Detail.AuditPolicy.SamplingRate != 0.25 {
		t.Errorf("auditPolicy.samplingRate = %v, want 0.25", got.Detail.AuditPolicy.SamplingRate)
	}
	if len(got.Detail.AuditPolicy.RedactFields) != 2 {
		t.Errorf("auditPolicy.redactFields = %v, want 2 entries", got.Detail.AuditPolicy.RedactFields)
	}

	if len(got.Detail.Sources.Produces) != 1 {
		t.Errorf("sources.produces = %d, want 1", len(got.Detail.Sources.Produces))
	}
}

const testAccConfigPlatformFull = `
provider "fianu" {}

resource "fianu_platform" "github" {
  path = "test.platform.github"
  name = "GitHub"

  detail = {
    description  = "GitHub SCM platform"
    display_logo = "github"
    website_url  = "https://github.com"
    docs_url     = "https://docs.github.com"
    logo_url     = "https://github.githubassets.com/logo.png"
    tool_version = "2024.11"

    platform_type = {
      name = "Source Control"
      uuid = "platform-type-uuid"
    }

    features = {
      webhooks = "true"
      apps     = "true"
    }

    sources = {
      produces = [
        { path = "github.repository.commit", note = "origin" },
      ]
    }

    endpoint_defaults = {
      base_url = "https://api.github.com"
      notes    = "Public GitHub; Enterprise instances override per-instance."
      default_headers = jsonencode({
        Accept = "application/vnd.github+json"
      })
    }

    health_checks = [
      {
        check_key         = "api_reachable"
        description       = "GitHub API answers and the token has quota"
        http_method       = "GET"
        endpoint_template = "/rate_limit"
        interval_seconds  = 60
        timeout_ms        = 5000
        retry_max         = 3
        retry_backoff_ms  = 250
        severity          = "critical"
        enabled           = true
        success_predicate = jsonencode({ status_in = [200] })
      },
    ]

    credential_policy = {
      rotation             = "proactive"
      grace_period_seconds = 300
      notes                = "App installation tokens expire hourly."
      reauth_triggers      = jsonencode({ on_status = [401] })
    }

    error_mappings = [
      {
        http_status    = 429
        classification = "rate_limit"
        action         = "backoff"
        is_terminal    = false
        endpoint_glob  = "/repos/*"
      },
      {
        provider_code  = "connection_reset"
        classification = "transient"
        action         = "retry"
        is_terminal    = false
      },
    ]

    audit_policy = {
      level              = "info"
      pii_handling       = "redact"
      redact_fields      = ["author_email", "committer_email"]
      retention_days     = 90
      export_destination = "s3://fianu-logs/github/"
      sampling_rate      = 0.25
      event_catalog      = jsonencode({ api_call = { level = "debug" } })
    }
  }
}
`

// TestAccFianuPlatform_Update proves an in-place change round-trips.
func TestAccFianuPlatform_Update(t *testing.T) {
	stub := newPlatformStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config: testAccConfigPlatformMinimal,
				Check:  resource.TestCheckResourceAttr("fianu_platform.github", "detail.description", "GitHub SCM platform"),
			},
			{
				Config: testAccConfigPlatformUpdated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("fianu_platform.github", "detail.description", "GitHub Enterprise Server"),
					resource.TestCheckResourceAttr("fianu_platform.github", "detail.endpoint_defaults.base_url", "https://ghe.internal/api/v3"),
				),
			},
		},
	})

	got := stub.captured(t)
	if got.Detail.Description != "GitHub Enterprise Server" {
		t.Errorf("after update, description = %q", got.Detail.Description)
	}
	if got.Detail.EndpointDefaults == nil || got.Detail.EndpointDefaults.BaseURL != "https://ghe.internal/api/v3" {
		t.Errorf("after update, endpointDefaults = %+v", got.Detail.EndpointDefaults)
	}
}

const testAccConfigPlatformUpdated = `
provider "fianu" {}

resource "fianu_platform" "github" {
  path = "test.platform.github"
  name = "GitHub"

  detail = {
    description = "GitHub Enterprise Server"
    endpoint_defaults = {
      base_url = "https://ghe.internal/api/v3"
    }
  }
}
`

// TestAccFianuPlatform_Import proves the composite ID round-trips.
func TestAccFianuPlatform_Import(t *testing.T) {
	stub := newPlatformStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{Config: testAccConfigPlatformMinimal},
			{
				Config:            testAccConfigPlatformMinimal,
				ResourceName:      "fianu_platform.github",
				ImportState:       true,
				ImportStateId:     "platform/test.platform.github",
				ImportStateVerify: true,
				// detail stays user-authored: the server stamps row ids and
				// platformVersionUuid onto each persisted sub-section, and
				// those aren't in the schema.
				ImportStateVerifyIgnore: []string{"detail"},
			},
		},
	})
}

// TestAccFianuPlatform_RejectsMalformedJSONAttribute pins the plan-time guard
// over the jsonencode-authored columns. Without it a typo deploys a platform
// whose health check has no success predicate — a probe that can never pass.
func TestAccFianuPlatform_RejectsMalformedJSONAttribute(t *testing.T) {
	stub := newPlatformStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigPlatformBadJSON,
				ExpectError: regexp.MustCompile(`value is not a JSON object`),
			},
		},
	})
}

const testAccConfigPlatformBadJSON = `
provider "fianu" {}

resource "fianu_platform" "github" {
  path = "test.platform.github"
  name = "GitHub"

  detail = {
    health_checks = [
      {
        check_key         = "api_reachable"
        http_method       = "GET"
        endpoint_template = "/rate_limit"
        success_predicate = "{status_in: 200"
      },
    ]
  }
}
`

// TestAccFianuPlatform_RejectsUnknownEnum proves the enum lists derived from
// the server's own constants are wired into validation.
func TestAccFianuPlatform_RejectsUnknownEnum(t *testing.T) {
	stub := newPlatformStub(t)
	defer stub.server.Close()
	setEnv(t, stub)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories(),
		Steps: []resource.TestStep{
			{
				Config:      testAccConfigPlatformBadEnum,
				ExpectError: regexp.MustCompile(`(?s)classification value must be one of`),
			},
		},
	})
}

const testAccConfigPlatformBadEnum = `
provider "fianu" {}

resource "fianu_platform" "github" {
  path = "test.platform.github"
  name = "GitHub"

  detail = {
    error_mappings = [
      {
        classification = "teapot"
        action         = "retry"
      },
    ]
  }
}
`

func setEnv(t *testing.T, stub *platformStub) {
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

// platformStub fakes Console for the platform resource.
type platformStub struct {
	server       *httptest.Server
	deployHits   atomic.Int32
	archiveHits  atomic.Int32
	archivedPath atomic.Value // string
	stored       atomic.Value // *transportv1.DeployEntityFileResponse
	capturedVal  atomic.Value // *fianu_entities.Platform
	lastCaptured atomic.Value // *fianu_entities.Platform, never cleared
}

func (s *platformStub) captured(t *testing.T) *fianu_entities.Platform {
	t.Helper()
	e, _ := s.lastCaptured.Load().(*fianu_entities.Platform)
	if e == nil {
		t.Fatal("no platform captured on the deploy route")
	}
	return e
}

func newPlatformStub(t *testing.T) *platformStub {
	t.Helper()
	stub := &platformStub{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/entities/artifacts/deploy", func(w http.ResponseWriter, r *http.Request) {
		stub.deployHits.Add(1)
		req, raw := decodeDeployRequest(r)

		var e fianu_entities.Platform
		if err := json.Unmarshal(raw, &e); err == nil {
			stub.capturedVal.Store(&e)
			stub.lastCaptured.Store(&e)
		}

		// The real endpoint's allowlist is keyed on General.entityType, so a
		// resource sending the wrong one would 400 in production.
		if req.General.EntityType == nil || *req.General.EntityType != string(db_vars.EntityTypePlatform) {
			http.Error(w, `{"error":"unsupported entity type"}`, http.StatusBadRequest)
			return
		}

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
				EntityID:    "test-platform-uuid",
				Path:        entityPath,
				Name:        e.Name,
				Version:     "1",
				EntityType:  string(db_vars.EntityTypePlatform),
			},
		}
		stub.stored.Store(resp)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// The generated FetchPlatform reads /api/platforms/:entity_key — the
	// canonical route sdkgen picked over the legacy /api/integrations one.
	mux.HandleFunc("/api/platforms/", func(w http.ResponseWriter, r *http.Request) {
		captured, _ := stub.capturedVal.Load().(*fianu_entities.Platform)
		if captured == nil {
			http.NotFound(w, r)
			return
		}
		out := *captured
		out.UUID = "test-platform-uuid"
		out.Type = db_vars.EntityTypePlatform
		out.Version.Semantic = "1"
		out.Version.UUID = "version-uuid"
		out.Version.Status = "active"
		out.Version.State = "published"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("/api/integrations/archive/platforms/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		stub.archiveHits.Add(1)
		stub.archivedPath.Store(r.URL.Path)
		stub.capturedVal.Store((*fianu_entities.Platform)(nil))
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
