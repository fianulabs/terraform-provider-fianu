// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package controltest implements the fianu_control_test Terraform Action.
//
// Actions (terraform-plugin-framework v1.16+, Terraform CLI v1.14+) are
// imperative, run-on-demand operations that don't persist state — the right
// primitive for "run my control's rego rules against my input/data fixtures
// and tell me if they pass."
//
// fianu_control_test maps directly to `fianu console test controls ./...`:
// same server endpoint (POST /entities/artifacts/test), same JUnit-shaped
// report. The HCL surface accepts an `evaluation` list that mirrors the
// fianu_control resource's `detail.evaluation`, so users typically share
// the cases via `locals` and reference them from both the resource and
// the action.
//
// Run on demand:
//
//	terraform apply -invoke=action.fianu_control_test.sast_checkmarx
//
// Or wire to apply via the resource's lifecycle.action_trigger block so
// the tests run automatically on every create/update.
package controltest

import (
	"context"
	"encoding/json"
	"fmt"

	fianu_types "github.com/fianulabs/core/v2/external/db/types/fianu"
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	sdk "github.com/fianulabs/core/v2/external/pkg/sdk/v2"
	pkgvariables "github.com/fianulabs/core/v2/external/pkg/variables"
	transportv1 "github.com/fianulabs/core/v2/external/transport/http/v1"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/joshdk/go-junit"
)

// Compile-time interface checks.
var (
	_ action.Action              = (*controlTestAction)(nil)
	_ action.ActionWithConfigure = (*controlTestAction)(nil)
)

// NewAction is the factory the provider package registers via Actions().
func NewAction() action.Action {
	return &controlTestAction{}
}

type controlTestAction struct {
	client *sdk.Client
}

// configModel is the HCL surface. `evaluation` mirrors the fianu_control
// resource's detail.evaluation list so users can share the same []case via
// `locals` between the resource and this action.
type configModel struct {
	Path       types.String          `tfsdk:"path"`
	Name       types.String          `tfsdk:"name"`
	EntityType types.String          `tfsdk:"entity_type"`
	Evaluation []evaluationCaseModel `tfsdk:"evaluation"`
}

type evaluationCaseModel struct {
	Type    types.String `tfsdk:"type"`
	Engine  types.String `tfsdk:"engine"`
	Label   types.String `tfsdk:"label"`
	Content types.String `tfsdk:"content"`
}

func (a *controlTestAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_control_test"
}

func (a *controlTestAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Runs a Fianu control's rego rules against its input/data fixtures via `POST /entities/artifacts/test`. Equivalent to `fianu console test controls`. Returns per-case pass/fail as progress events; sets diagnostics if any case fails.",
		Attributes: map[string]schema.Attribute{
			"path": schema.StringAttribute{
				MarkdownDescription: "Entity key of the control to test (e.g., `checkmarx.sast.vulnerabilities`). Same value as the resource's `path`.",
				Required:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Display name of the control. Mirrors the resource's `name`.",
				Required:            true,
			},
			"entity_type": schema.StringAttribute{
				MarkdownDescription: "Entity type — defaults to `control`. Set to `gate_control` to test a gate's rules.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.OneOf(string(db_vars.EntityTypeControl), string(db_vars.EntityTypeGateControl)),
				},
			},
			"evaluation": schema.ListNestedAttribute{
				MarkdownDescription: "Evaluation cases — must include at least one `rule` case plus the `input`/`data` test fixtures the rule should be evaluated against. Same shape as fianu_control.detail.evaluation; share via `locals` for DRY.",
				Required:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"type": schema.StringAttribute{
							Required: true,
							Validators: []validator.String{
								stringvalidator.OneOf(
									string(pkgvariables.CRRule),
									string(pkgvariables.CRRuleTest),
									string(pkgvariables.CRDetail),
									string(pkgvariables.CRDisplay),
									string(pkgvariables.CRReport),
									string(pkgvariables.CRInput),
									string(pkgvariables.CRData),
								),
							},
						},
						"engine":  schema.StringAttribute{Optional: true},
						"label":   schema.StringAttribute{Optional: true},
						"content": schema.StringAttribute{Required: true},
					},
				},
			},
		},
	}
}

func (a *controlTestAction) Configure(_ context.Context, req action.ConfigureRequest, resp *action.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*sdk.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"unexpected provider data",
			fmt.Sprintf("expected *sdk.Client, got %T. This is a provider bug.", req.ProviderData),
		)
		return
	}
	a.client = client
}

// Invoke is the action's entry point. Reads HCL config, then hands off to
// the typed-config helper so the testable logic isn't entangled with the
// framework's Config plumbing.
func (a *controlTestAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var cfg configModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a.invokeWithConfig(ctx, cfg, resp)
}

// invokeWithConfig is the testable seam. Same logic as Invoke minus the
// HCL→model parsing — unit tests construct configModel directly so they
// don't have to fake tfsdk.Config internals.
func (a *controlTestAction) invokeWithConfig(ctx context.Context, cfg configModel, resp *action.InvokeResponse) {
	entityType := db_vars.EntityTypeControl
	if !cfg.EntityType.IsNull() && !cfg.EntityType.IsUnknown() && cfg.EntityType.ValueString() != "" {
		entityType = db_vars.EntityType(cfg.EntityType.ValueString())
	}

	entity := buildTestEntity(cfg, entityType)
	resp.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("Testing %s %q with %d evaluation cases…", entityType, cfg.Path.ValueString(), len(cfg.Evaluation)),
	})

	entityJSON, err := json.Marshal(entity)
	if err != nil {
		resp.Diagnostics.AddError("marshal entity failed", err.Error())
		return
	}
	entityTypeStr := string(entityType)
	pathStr := cfg.Path.ValueString()
	testReq := transportv1.DeployEntityFileRequest{
		General: fianu_types.General{
			EntityType: &entityTypeStr,
			Path:       &pathStr,
		},
	}
	testResp, err := a.client.TestEntityFile(ctx, testReq, entityJSON)
	if err != nil {
		resp.Diagnostics.AddError(fmt.Sprintf("test %s failed", entityType), err.Error())
		return
	}

	emitTestReport(testResp, resp)
}

// buildTestEntity produces the minimal Control payload TestEntity needs.
// The server's test pipeline only inspects the evaluation cases (rule +
// input + data); the rest of the Detail is irrelevant for testing.
func buildTestEntity(cfg configModel, entityType db_vars.EntityType) *fianu_entities.Control {
	c := &fianu_entities.Control{}
	c.Path = cfg.Path.ValueString()
	c.Name = cfg.Name.ValueString()
	c.Type = entityType
	c.Detail.Evaluation = make([]fianu_entities.Case, len(cfg.Evaluation))
	for i, ec := range cfg.Evaluation {
		c.Detail.Evaluation[i] = fianu_entities.Case{
			CaseBody: fianu_entities.CaseBody{
				Type:    pkgvariables.ControlResource(ec.Type.ValueString()),
				Label:   ec.Label.ValueString(),
				Enabled: true,
			},
			Detail: []byte(ec.Content.ValueString()),
		}
	}
	return c
}

// emitTestReport walks the report the server returns and sends one progress
// event per test case so the user sees per-case results streaming as they
// arrive. If any case failed — or none ran at all — surfaces a single error
// diagnostic at the end so the action exits non-zero.
func emitTestReport(testResp any, resp *action.InvokeResponse) {
	// Marshal the response to JSON, then walk the parsed structure. This
	// avoids importing transportv1 here and keeps the action's coupling to
	// the SDK shallow.
	raw, err := json.Marshal(testResp)
	if err != nil {
		resp.Diagnostics.AddWarning("test report unparseable", err.Error())
		return
	}
	var envelope struct {
		Report   junitSuite `json:"report"`
		EntityID string     `json:"entityId"`
		Path     string     `json:"path"`
		Name     string     `json:"name"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		resp.Diagnostics.AddWarning("test report unparseable", err.Error())
		return
	}

	cases := collectTests(envelope.Report, nil)
	failures := 0
	for _, c := range cases {
		label := c.Name
		if label == "" {
			label = c.Classname
		}
		switch c.Status {
		case junit.StatusPassed:
			resp.SendProgress(action.InvokeProgressEvent{Message: fmt.Sprintf("✓ %s", label)})
		case junit.StatusSkipped:
			resp.SendProgress(action.InvokeProgressEvent{Message: fmt.Sprintf("⊘ %s (skipped): %s", label, c.detail())})
		default:
			// failed, error, and any status the server adds later. Defaulting
			// unknown to "failed" keeps a test action that can't interpret its
			// own report loud rather than green.
			failures++
			resp.SendProgress(action.InvokeProgressEvent{
				Message: fmt.Sprintf("✗ %s [%s]: %s", label, c.Status, c.detail()),
			})
		}
	}

	if len(cases) == 0 {
		resp.Diagnostics.AddError(
			"no test cases ran",
			fmt.Sprintf("Path: %s — the server returned a report with no test cases. "+
				"The control package has no `rule_test` evaluation cases, or none of them were executed. "+
				"A test action that runs nothing is reported as a failure rather than a pass.", envelope.Path),
		)
		return
	}

	resp.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("%d/%d cases passed for %s", len(cases)-failures, len(cases), envelope.Path),
	})

	if failures > 0 {
		resp.Diagnostics.AddError(
			fmt.Sprintf("%d/%d test cases failed", failures, len(cases)),
			fmt.Sprintf("Path: %s — see progress events above for per-case detail.", envelope.Path),
		)
	}
}

// collectTests flattens a report into its test cases.
//
// The server nests one level today (`report.suites[].tests[]` from
// ControlTester, `report.tests[]` from PolicyTester), but both levels carry
// cases and nothing guarantees the depth stays at one, so recurse.
func collectTests(s junitSuite, out []junitTest) []junitTest {
	out = append(out, s.Tests...)
	for _, sub := range s.Suites {
		out = collectTests(sub, out)
	}
	return out
}

// junitSuite mirrors the wire shape of the server's report: `testing.TestReport`
// (external/db/types/fianu/testing/v1.0.0), which is a `junit.Suite` from
// github.com/joshdk/go-junit. The top-level `report` object is itself a suite.
//
// These are hand-declared rather than reusing junit.Suite because junit.Test
// types its `error` field as the `error` interface, which json.Unmarshal
// cannot populate — decoding a failed case into the upstream type errors out.
// The status constants are imported from upstream, though; they are the part
// that silently inverts results if it drifts.
type junitSuite struct {
	Name   string       `json:"name"`
	Tests  []junitTest  `json:"tests"`
	Suites []junitSuite `json:"suites"`
}

type junitTest struct {
	Name      string       `json:"name"`
	Classname string       `json:"classname"`
	Status    junit.Status `json:"status"`
	Message   string       `json:"message"`
	Error     *junitError  `json:"error"`
}

type junitError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// detail is the most specific failure text available. Message is the
// human-facing summary; Error.Message carries the underlying cause and is
// only set on failed/error cases.
func (t junitTest) detail() string {
	switch {
	case t.Error != nil && t.Error.Message != "" && t.Error.Message != t.Message:
		if t.Message == "" {
			return t.Error.Message
		}
		return fmt.Sprintf("%s (%s)", t.Message, t.Error.Message)
	case t.Message != "":
		return t.Message
	default:
		return "no detail reported"
	}
}
