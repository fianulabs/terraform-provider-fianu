package controltest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	fianu "github.com/fianulabs/core/v2/external/pkg/clients/fianu"
	"github.com/fianulabs/core/v2/external/pkg/connections"
	transportv1 "github.com/fianulabs/core/v2/external/transport/http/v1"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAction_InvokeStreamsPerCaseProgress proves the action's invoke logic
// walks the JUnit-shaped report the server returns and emits a progress
// event for every case. Hits the existing /entities/artifacts/test endpoint
// via the SDK; CLI-level acceptance support landed in terraform-plugin-
// testing newer than our pin, so this is unit-level.
func TestAction_InvokeStreamsPerCaseProgress(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/entities/artifacts/test", r.URL.Path)
		_ = json.NewEncoder(w).Encode(transportv1.TestEntityFileResponse{
			Path: "checkmarx.sast.vulnerabilities",
			Name: "SAST",
			Report: map[string]any{
				"testsuites": []map[string]any{
					{
						"name": "rule_test.rego",
						"testcase": []map[string]any{
							{"name": "occ_case_1", "classname": "rule"},
							{"name": "occ_case_2", "classname": "rule"},
						},
					},
				},
			},
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

// TestAction_InvokeFailsOnFailedCase proves a failure in the JUnit report
// surfaces as an error diagnostic — the signal `terraform action` and CI
// systems use to mark the run failed.
func TestAction_InvokeFailsOnFailedCase(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(transportv1.TestEntityFileResponse{
			Path: "checkmarx.sast.vulnerabilities",
			Name: "SAST",
			Report: map[string]any{
				"testsuites": []map[string]any{
					{
						"name": "rule_test.rego",
						"testcase": []map[string]any{
							{"name": "occ_case_1", "classname": "rule"},
							{
								"name":      "occ_case_fail",
								"classname": "rule",
								"failure":   map[string]any{"message": "expected pass, got fail"},
							},
						},
					},
				},
			},
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
		if contains(m, "✗") {
			sawFailureMarker = true
		}
	}
	assert.True(t, sawFailureMarker, "failure case must emit a ✗-marked progress event; got %v", got)
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
	u, err := url.Parse(serverURL)
	require.NoError(t, err)

	client := fianu.NewClient(
		fianu.WithConsole(connections.NewBase(u)),
		fianu.WithBearerAuth("test-token"),
	)

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

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
