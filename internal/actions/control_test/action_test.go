// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package controltest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	fianutesting "github.com/fianulabs/core/v2/external/db/types/fianu/testing/v1.0.0"
	sdk "github.com/fianulabs/core/v2/external/pkg/sdk/v2"
	transportv1 "github.com/fianulabs/core/v2/external/transport/http/v1"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/joshdk/go-junit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reportFixture builds the exact wire shape the server emits: `report` is a
// `testing.TestReport` (external/db/types/fianu/testing/v1.0.0), which is a
// `junit.Suite` — one suite per control, with the cases under `tests`.
//
// Built from the upstream types rather than hand-written maps on purpose. The
// bug this test suite previously missed was that the action parsed
// `testsuites`/`testcase`/`failure` — JUnit *XML* element names — while the
// server marshals Go structs whose tags are `suites`/`tests`/`status`. Hand
// written fixtures let both halves be wrong in agreement; marshalling the real
// type cannot.
func reportFixture(suiteName string, tests ...junit.Test) *fianutesting.TestReport {
	r := fianutesting.NewTestReport("SAST", map[string]string{"entityType": "control"})
	r.Suites = append(r.Suites, junit.Suite{Name: suiteName, Tests: tests})
	closed := r.Close()
	return &closed
}

func passingCase(name string) junit.Test {
	return junit.Test{Name: name, Classname: "rule", Status: junit.StatusPassed, Message: "ok"}
}

// TestAction_InvokeStreamsPerCaseProgress proves the action's invoke logic
// walks the report the server returns and emits a progress event for every
// case. Hits the existing /entities/artifacts/test endpoint via the SDK;
// CLI-level acceptance support landed in terraform-plugin-testing newer than
// our pin, so this is unit-level.
func TestAction_InvokeStreamsPerCaseProgress(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/entities/artifacts/test", r.URL.Path)
		_ = json.NewEncoder(w).Encode(transportv1.TestEntityFileResponse{
			Path:   "checkmarx.sast.vulnerabilities",
			Name:   "SAST",
			Report: reportFixture("checkmarx.sast.vulnerabilities-tests", passingCase("occ_case_1"), passingCase("occ_case_2")),
		})
	}))
	defer srv.Close()

	a, resp, events := newActionForTest(t, srv.URL)
	a.invokeWithConfig(context.Background(), simpleConfig("checkmarx.sast.vulnerabilities", "SAST"), resp)

	assert.False(t, resp.Diagnostics.HasError(), "successful test run must not produce error diagnostics: %v", resp.Diagnostics)

	got := events.collected()
	require.GreaterOrEqual(t, len(got), 3, "expected initial + per-case + summary events; got %v", got)
	assert.Contains(t, got[len(got)-1], "2/2 cases passed")
}

// TestAction_InvokeFailsOnFailedCase proves a failed case surfaces as an error
// diagnostic — the signal `terraform apply -invoke` and CI systems use to mark
// the run failed.
func TestAction_InvokeFailsOnFailedCase(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(transportv1.TestEntityFileResponse{
			Path: "checkmarx.sast.vulnerabilities",
			Name: "SAST",
			Report: reportFixture("rule_test.rego",
				passingCase("occ_case_1"),
				junit.Test{
					Name:      "occ_case_fail",
					Classname: "rule",
					Status:    junit.StatusFailed,
					Message:   "expected pass, got fail",
					Error:     junit.Error{Message: "assertion failed", Type: "AssertionError"},
				},
			),
		})
	}))
	defer srv.Close()

	a, resp, events := newActionForTest(t, srv.URL)
	a.invokeWithConfig(context.Background(), simpleConfig("x", "X"), resp)

	require.True(t, resp.Diagnostics.HasError(), "failed case must surface as error diagnostic")
	assert.Contains(t, resp.Diagnostics.Errors()[0].Summary(), "1/2 test cases failed")

	got := events.collected()
	var sawFailureMarker bool
	for _, m := range got {
		if strings.Contains(m, "✗ occ_case_fail") && strings.Contains(m, "assertion failed") {
			sawFailureMarker = true
		}
	}
	assert.True(t, sawFailureMarker, "failure case must emit a ✗-marked progress event carrying the cause; got %v", got)
}

// TestAction_InvokeFailsOnErroredCase proves status "error" — a test that blew
// up rather than asserting false — is counted as a failure. The previous
// implementation looked only for a `failure` child and rendered these as ✓.
func TestAction_InvokeFailsOnErroredCase(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(transportv1.TestEntityFileResponse{
			Path: "x",
			Report: reportFixture("rule_test.rego", junit.Test{
				Name:    "native_test_execution",
				Status:  junit.StatusError,
				Message: "Failed to execute native rego tests",
				Error:   junit.Error{Message: "rego_parse_error", Type: "NativeTestError"},
			}),
		})
	}))
	defer srv.Close()

	a, resp, _ := newActionForTest(t, srv.URL)
	a.invokeWithConfig(context.Background(), simpleConfig("x", "X"), resp)

	require.True(t, resp.Diagnostics.HasError(), "errored case must surface as error diagnostic")
	assert.Contains(t, resp.Diagnostics.Errors()[0].Summary(), "1/1 test cases failed")
}

// TestAction_InvokeReadsTopLevelTests proves cases sitting directly on the
// report — the shape PolicyTester emits — are counted. They live on
// `report.tests`, not inside a suite.
func TestAction_InvokeReadsTopLevelTests(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		report := fianutesting.NewTestReport("PolicyValidation", map[string]string{"entityType": "policy"})
		report.Tests = append(report.Tests, junit.Test{
			Name:      "policy_validation",
			Classname: "policy",
			Status:    junit.StatusPassed,
			Message:   "Policy validation successful",
		})
		closed := report.Close()
		_ = json.NewEncoder(w).Encode(transportv1.TestEntityFileResponse{Path: "x", Report: &closed})
	}))
	defer srv.Close()

	a, resp, events := newActionForTest(t, srv.URL)
	a.invokeWithConfig(context.Background(), simpleConfig("x", "X"), resp)

	assert.False(t, resp.Diagnostics.HasError(), "passing top-level case must not error: %v", resp.Diagnostics)
	assert.Contains(t, events.collected()[len(events.collected())-1], "1/1 cases passed")
}

// TestAction_InvokeFailsWhenNothingRan proves an empty report is a failure.
// A report with no cases means no test executed — reporting that as a pass is
// how a broken test step stays green forever.
func TestAction_InvokeFailsWhenNothingRan(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		report := fianutesting.NewTestReport("SAST", nil)
		closed := report.Close()
		_ = json.NewEncoder(w).Encode(transportv1.TestEntityFileResponse{Path: "x", Report: &closed})
	}))
	defer srv.Close()

	a, resp, _ := newActionForTest(t, srv.URL)
	a.invokeWithConfig(context.Background(), simpleConfig("x", "X"), resp)

	require.True(t, resp.Diagnostics.HasError(), "a report with zero cases must not be reported as success")
	assert.Contains(t, resp.Diagnostics.Errors()[0].Summary(), "no test cases ran")
}

// TestAction_InvokeSurfacesServerError proves a 4xx/5xx from the server
// becomes an error diagnostic instead of being swallowed.
func TestAction_InvokeSurfacesServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"something broke"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	a, resp, _ := newActionForTest(t, srv.URL)
	a.invokeWithConfig(context.Background(), simpleConfig("x", "X"), resp)

	require.True(t, resp.Diagnostics.HasError())
	assert.Contains(t, resp.Diagnostics.Errors()[0].Summary(), "test")
}

// progressCollector is a thread-safe sink for InvokeProgressEvent messages
// so tests can assert on what the action emitted to the CLI.
type progressCollector struct {
	mu     sync.Mutex
	events []string
}

func (p *progressCollector) capture(e action.InvokeProgressEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e.Message)
}

func (p *progressCollector) collected() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.events))
	copy(out, p.events)
	return out
}

func newActionForTest(t *testing.T, serverURL string) (*controlTestAction, *action.InvokeResponse, *progressCollector) {
	t.Helper()

	client, err := sdk.NewClient(
		sdk.WithBaseURL(serverURL),
		sdk.WithBearerToken("test-token"),
	)
	require.NoError(t, err)

	collector := &progressCollector{}
	resp := &action.InvokeResponse{
		SendProgress: collector.capture,
	}

	return &controlTestAction{client: client}, resp, collector
}

// simpleConfig builds a minimal configModel suitable for unit tests:
// path, name, and a single rule case.
func simpleConfig(path, name string) configModel {
	return configModel{
		Path: types.StringValue(path),
		Name: types.StringValue(name),
		Evaluation: []evaluationCaseModel{{
			Type:    types.StringValue("rule"),
			Engine:  types.StringValue("opa"),
			Label:   types.StringValue("rule.rego"),
			Content: types.StringValue("package rule\ndefault pass = false\n"),
		}},
	}
}
