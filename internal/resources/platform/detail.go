// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package platform

import (
	"encoding/json"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The five optional PlatformDetail sub-sections. Each maps 1:1 to a row (or
// rows) in the platform's version tables, so the shapes here are the server's,
// minus two fields that appear on every one of them:
//
//   - `id` — the database row's surrogate key, assigned on insert.
//   - `platformVersionUuid` — the FK back to the version being written, which
//     the deployer fills in from the entity it just built.
//
// Accepting either from HCL would let a config claim a row identity it doesn't
// own. They are omitted, not hidden: nothing a user can express is lost.
//
// Free-form JSON columns (`defaultHeaders`, `headerTemplate`, `bodyTemplate`,
// `successPredicate`, `reauthTriggers`, `eventCatalog`) are strings authored
// with jsonencode, the same treatment fianu_entity_pod.value gets — their
// shapes are provider-defined and can't be typed in HCL.

// Enum value lists, taken from external/db/variables/types.go rather than
// restated as literals, so a new server-side value is a compile error here
// instead of a silent plan-time rejection.
var (
	healthSeverities = []string{
		string(db_vars.PlatformHealthSeverityPending),
		string(db_vars.PlatformHealthSeverityInfo),
		string(db_vars.PlatformHealthSeverityWarning),
		string(db_vars.PlatformHealthSeverityError),
		string(db_vars.PlatformHealthSeverityCritical),
	}
	rotationModes = []string{
		string(db_vars.RotationModeProactive),
		string(db_vars.RotationModeReactive),
		string(db_vars.RotationModeManual),
		string(db_vars.RotationModeDisabled),
	}
	errorClasses = []string{
		string(db_vars.PlatformErrorClassAuth),
		string(db_vars.PlatformErrorClassPermission),
		string(db_vars.PlatformErrorClassRateLimit),
		string(db_vars.PlatformErrorClassQuota),
		string(db_vars.PlatformErrorClassNotFound),
		string(db_vars.PlatformErrorClassTransient),
		string(db_vars.PlatformErrorClassInvalidRequest),
		string(db_vars.PlatformErrorClassConflict),
		string(db_vars.PlatformErrorClassServer),
		string(db_vars.PlatformErrorClassUnknown),
	}
	errorActions = []string{
		string(db_vars.PlatformErrorActionRetry),
		string(db_vars.PlatformErrorActionFail),
		string(db_vars.PlatformErrorActionIgnore),
		string(db_vars.PlatformErrorActionReauth),
		string(db_vars.PlatformErrorActionBackoff),
	}
	logLevels = []string{
		string(db_vars.PlatformLogLevelDebug),
		string(db_vars.PlatformLogLevelInfo),
		string(db_vars.PlatformLogLevelWarning),
		string(db_vars.PlatformLogLevelError),
		string(db_vars.PlatformLogLevelCritical),
	}
	piiModes = []string{
		string(db_vars.PlatformPIIModeRedact),
		string(db_vars.PlatformPIIModeHash),
		string(db_vars.PlatformPIIModeEncrypt),
		string(db_vars.PlatformPIIModeAllow),
	}
)

type endpointDefaultsModel struct {
	BaseURL        types.String `tfsdk:"base_url"`
	DefaultHeaders types.String `tfsdk:"default_headers"`
	Notes          types.String `tfsdk:"notes"`
}

type healthCheckModel struct {
	CheckKey         types.String `tfsdk:"check_key"`
	Description      types.String `tfsdk:"description"`
	HTTPMethod       types.String `tfsdk:"http_method"`
	EndpointTemplate types.String `tfsdk:"endpoint_template"`
	HeaderTemplate   types.String `tfsdk:"header_template"`
	BodyTemplate     types.String `tfsdk:"body_template"`
	SuccessPredicate types.String `tfsdk:"success_predicate"`
	IntervalSeconds  types.Int64  `tfsdk:"interval_seconds"`
	TimeoutMs        types.Int64  `tfsdk:"timeout_ms"`
	RetryMax         types.Int64  `tfsdk:"retry_max"`
	RetryBackoffMs   types.Int64  `tfsdk:"retry_backoff_ms"`
	Severity         types.String `tfsdk:"severity"`
	Enabled          types.Bool   `tfsdk:"enabled"`
}

type credentialPolicyModel struct {
	Rotation           types.String `tfsdk:"rotation"`
	GracePeriodSeconds types.Int64  `tfsdk:"grace_period_seconds"`
	ReauthTriggers     types.String `tfsdk:"reauth_triggers"`
	Notes              types.String `tfsdk:"notes"`
}

type errorMappingModel struct {
	HTTPStatus     types.Int64  `tfsdk:"http_status"`
	ProviderCode   types.String `tfsdk:"provider_code"`
	ContainsText   types.String `tfsdk:"contains_text"`
	EndpointGlob   types.String `tfsdk:"endpoint_glob"`
	Classification types.String `tfsdk:"classification"`
	Action         types.String `tfsdk:"action"`
	IsTerminal     types.Bool   `tfsdk:"is_terminal"`
	Notes          types.String `tfsdk:"notes"`
}

type auditPolicyModel struct {
	Level             types.String  `tfsdk:"level"`
	PIIHandling       types.String  `tfsdk:"pii_handling"`
	RedactFields      []string      `tfsdk:"redact_fields"`
	EventCatalog      types.String  `tfsdk:"event_catalog"`
	RetentionDays     types.Int64   `tfsdk:"retention_days"`
	ExportDestination types.String  `tfsdk:"export_destination"`
	SamplingRate      types.Float64 `tfsdk:"sampling_rate"`
}

func endpointDefaultsAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Default HTTP endpoint configuration every instance of this platform inherits.",
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"base_url": schema.StringAttribute{
				MarkdownDescription: "Canonical public base endpoint, e.g. `https://api.github.com`.",
				Required:            true,
			},
			"default_headers": schema.StringAttribute{
				MarkdownDescription: "Headers sent on every request, as a JSON object. Author with `jsonencode({ Accept = \"application/vnd.github+json\" })`.",
				Optional:            true,
			},
			"notes": schema.StringAttribute{
				MarkdownDescription: "Free-form operator notes.",
				Optional:            true,
			},
		},
	}
}

func healthChecksAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "Probes that decide whether an instance of this platform is usable.",
		Optional:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"check_key": schema.StringAttribute{
					MarkdownDescription: "Stable identifier for this check, e.g. `api_reachable`, `token_valid`.",
					Required:            true,
				},
				"description": schema.StringAttribute{
					MarkdownDescription: "What this check proves.",
					Optional:            true,
				},
				"http_method": schema.StringAttribute{
					MarkdownDescription: "HTTP method for the probe request.",
					Required:            true,
					Validators: []validator.String{
						stringvalidator.OneOf("GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"),
					},
				},
				"endpoint_template": schema.StringAttribute{
					MarkdownDescription: "Path appended to the platform's base URL, e.g. `/rate_limit`.",
					Required:            true,
				},
				"header_template": schema.StringAttribute{
					MarkdownDescription: "Request headers as a JSON object. Author with `jsonencode({...})`.",
					Optional:            true,
				},
				"body_template": schema.StringAttribute{
					MarkdownDescription: "Request body as a JSON object. Author with `jsonencode({...})`.",
					Optional:            true,
				},
				"success_predicate": schema.StringAttribute{
					MarkdownDescription: "JSON predicate deciding success, e.g. `jsonencode({ status_in = [200] })`.",
					Optional:            true,
				},
				"interval_seconds": schema.Int64Attribute{
					MarkdownDescription: "How often to run the check.",
					Optional:            true,
				},
				"timeout_ms": schema.Int64Attribute{
					MarkdownDescription: "Per-attempt timeout in milliseconds.",
					Optional:            true,
				},
				"retry_max": schema.Int64Attribute{
					MarkdownDescription: "Maximum retries before the check is considered failed.",
					Optional:            true,
				},
				"retry_backoff_ms": schema.Int64Attribute{
					MarkdownDescription: "Backoff between retries in milliseconds.",
					Optional:            true,
				},
				"severity": schema.StringAttribute{
					MarkdownDescription: "How bad a failure of this check is. One of `pending`, `info`, `warning`, `error`, `critical`.",
					Optional:            true,
					Validators:          []validator.String{stringvalidator.OneOf(healthSeverities...)},
				},
				"enabled": schema.BoolAttribute{
					MarkdownDescription: "Whether this check runs. Omitted means off, matching the server's zero value.",
					Optional:            true,
				},
			},
		},
	}
}

func credentialPolicyAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "How credentials for this platform are rotated and when re-authentication is forced.",
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"rotation": schema.StringAttribute{
				MarkdownDescription: "Rotation stance. One of `proactive`, `reactive`, `manual`, `disabled`.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf(rotationModes...)},
			},
			"grace_period_seconds": schema.Int64Attribute{
				MarkdownDescription: "Clock-skew buffer applied around credential expiry.",
				Optional:            true,
			},
			"reauth_triggers": schema.StringAttribute{
				MarkdownDescription: "Conditions that force re-authentication, as a JSON object. Author with `jsonencode({...})`.",
				Optional:            true,
			},
			"notes": schema.StringAttribute{
				MarkdownDescription: "Free-form operator notes.",
				Optional:            true,
			},
		},
	}
}

func errorMappingsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "Translates provider-specific failures into Fianu's error semantics, so the collector knows whether to retry, back off, re-auth, or give up.",
		Optional:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"http_status": schema.Int64Attribute{
					MarkdownDescription: "HTTP status this rule matches. Omit for non-HTTP failures.",
					Optional:            true,
				},
				"provider_code": schema.StringAttribute{
					MarkdownDescription: "Provider-specific error code this rule matches.",
					Optional:            true,
				},
				"contains_text": schema.StringAttribute{
					MarkdownDescription: "Substring match against the response body or message.",
					Optional:            true,
				},
				"endpoint_glob": schema.StringAttribute{
					MarkdownDescription: "Endpoint pattern this rule is scoped to, e.g. `/repos/*`.",
					Optional:            true,
				},
				"classification": schema.StringAttribute{
					MarkdownDescription: "Fianu error class. One of `auth`, `permission`, `rate_limit`, `quota`, `not_found`, `transient`, `invalid_request`, `conflict`, `server`, `unknown`.",
					Required:            true,
					Validators:          []validator.String{stringvalidator.OneOf(errorClasses...)},
				},
				"action": schema.StringAttribute{
					MarkdownDescription: "What to do when this rule matches. One of `retry`, `fail`, `ignore`, `reauth`, `backoff`.",
					Required:            true,
					Validators:          []validator.String{stringvalidator.OneOf(errorActions...)},
				},
				"is_terminal": schema.BoolAttribute{
					MarkdownDescription: "Whether this failure ends the operation rather than being retried.",
					Optional:            true,
				},
				"notes": schema.StringAttribute{
					MarkdownDescription: "Free-form operator notes.",
					Optional:            true,
				},
			},
		},
	}
}

func auditPolicyAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Logging, PII handling and retention for this platform's traffic.",
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"level": schema.StringAttribute{
				MarkdownDescription: "Log level. One of `debug`, `info`, `warning`, `error`, `critical`.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf(logLevels...)},
			},
			"pii_handling": schema.StringAttribute{
				MarkdownDescription: "How personally identifiable information in logs is treated. One of `redact`, `hash`, `encrypt`, `allow`.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf(piiModes...)},
			},
			"redact_fields": schema.ListAttribute{
				MarkdownDescription: "Field names to redact before logs are written.",
				Optional:            true,
				ElementType:         types.StringType,
			},
			"event_catalog": schema.StringAttribute{
				MarkdownDescription: "Structured event catalog as a JSON object. Author with `jsonencode({...})`.",
				Optional:            true,
			},
			"retention_days": schema.Int64Attribute{
				MarkdownDescription: "How long logs are kept.",
				Optional:            true,
			},
			"export_destination": schema.StringAttribute{
				MarkdownDescription: "Where logs are shipped, e.g. `s3://fianu-logs/github/`.",
				Optional:            true,
			},
			"sampling_rate": schema.Float64Attribute{
				MarkdownDescription: "Fraction of events logged, between 0.0 and 1.0.",
				Optional:            true,
				Validators: []validator.Float64{
					float64validator.AtLeast(0),
					float64validator.AtMost(1),
				},
			},
		},
	}
}

func buildEndpointDefaults(in *endpointDefaultsModel) *fianu_entities.PlatformEndpointDefaults {
	if in == nil {
		return nil
	}
	return &fianu_entities.PlatformEndpointDefaults{
		BaseURL:        in.BaseURL.ValueString(),
		DefaultHeaders: decodeJSONObject(in.DefaultHeaders),
		Notes:          in.Notes.ValueString(),
	}
}

func buildHealthChecks(in []healthCheckModel) []fianu_entities.PlatformHealthCheck {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.PlatformHealthCheck, 0, len(in))
	for _, h := range in {
		out = append(out, fianu_entities.PlatformHealthCheck{
			CheckKey:         h.CheckKey.ValueString(),
			Description:      h.Description.ValueString(),
			HTTPMethod:       h.HTTPMethod.ValueString(),
			EndpointTemplate: h.EndpointTemplate.ValueString(),
			HeaderTemplate:   decodeJSONObject(h.HeaderTemplate),
			BodyTemplate:     decodeJSONObject(h.BodyTemplate),
			SuccessPredicate: decodeJSONObject(h.SuccessPredicate),
			IntervalSeconds:  int(h.IntervalSeconds.ValueInt64()),
			TimeoutMs:        int(h.TimeoutMs.ValueInt64()),
			RetryMax:         int(h.RetryMax.ValueInt64()),
			RetryBackoffMs:   int(h.RetryBackoffMs.ValueInt64()),
			Severity:         db_vars.PlatformHealthSeverity(h.Severity.ValueString()),
			Enabled:          h.Enabled.ValueBool(),
		})
	}
	return out
}

func buildCredentialPolicy(in *credentialPolicyModel) *fianu_entities.PlatformCredentialPolicy {
	if in == nil {
		return nil
	}
	return &fianu_entities.PlatformCredentialPolicy{
		Rotation:           db_vars.RotationMode(in.Rotation.ValueString()),
		GracePeriodSeconds: int(in.GracePeriodSeconds.ValueInt64()),
		ReauthTriggers:     decodeJSONObject(in.ReauthTriggers),
		Notes:              in.Notes.ValueString(),
	}
}

func buildErrorMappings(in []errorMappingModel) []fianu_entities.PlatformErrorMapping {
	if len(in) == 0 {
		return nil
	}
	out := make([]fianu_entities.PlatformErrorMapping, 0, len(in))
	for _, m := range in {
		mapping := fianu_entities.PlatformErrorMapping{
			ProviderCode:   m.ProviderCode.ValueString(),
			ContainsText:   m.ContainsText.ValueString(),
			EndpointGlob:   m.EndpointGlob.ValueString(),
			Classification: db_vars.PlatformErrorClass(m.Classification.ValueString()),
			Action:         db_vars.PlatformErrorAction(m.Action.ValueString()),
			IsTerminal:     m.IsTerminal.ValueBool(),
			Notes:          m.Notes.ValueString(),
		}
		// HTTPStatus is a *int on the wire precisely so a non-HTTP failure can
		// say "no status" rather than "status 0" — keep null null.
		if v := m.HTTPStatus; !v.IsNull() && !v.IsUnknown() {
			status := int(v.ValueInt64())
			mapping.HTTPStatus = &status
		}
		out = append(out, mapping)
	}
	return out
}

func buildAuditPolicy(in *auditPolicyModel) *fianu_entities.PlatformAuditPolicy {
	if in == nil {
		return nil
	}
	return &fianu_entities.PlatformAuditPolicy{
		Level:             db_vars.PlatformLogLevel(in.Level.ValueString()),
		PIIHandling:       db_vars.PlatformPIIMode(in.PIIHandling.ValueString()),
		RedactFields:      in.RedactFields,
		EventCatalog:      decodeJSONObject(in.EventCatalog),
		RetentionDays:     int(in.RetentionDays.ValueInt64()),
		ExportDestination: in.ExportDestination.ValueString(),
		SamplingRate:      in.SamplingRate.ValueFloat64(),
	}
}

// decodeJSONObject parses a jsonencode-authored attribute. Unparseable input
// can't reach here — validateJSONObjectAttributes rejects it at plan time —
// so a decode failure yields nil rather than a second error path.
func decodeJSONObject(v types.String) map[string]interface{} {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(v.ValueString()), &out); err != nil {
		return nil
	}
	return out
}

// validateJSONObjectAttributes checks every jsonencode-authored attribute in
// the detail at plan time. Without it a typo'd jsonencode silently deploys a
// platform with that column empty, which surfaces much later as a health check
// that never passes or an audit policy that logs nothing.
func validateJSONObjectAttributes(d platformDetailModel) diag.Diagnostics {
	var diags diag.Diagnostics
	root := path.Root("detail")

	check := func(p path.Path, v types.String) {
		if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
			return
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(v.ValueString()), &parsed); err != nil {
			diags.AddAttributeError(p, "value is not a JSON object",
				"Author this attribute with jsonencode({...}). Parse error: "+err.Error())
		}
	}

	if d.EndpointDefaults != nil {
		check(root.AtName("endpoint_defaults").AtName("default_headers"), d.EndpointDefaults.DefaultHeaders)
	}
	for i, h := range d.HealthChecks {
		hp := root.AtName("health_checks").AtListIndex(i)
		check(hp.AtName("header_template"), h.HeaderTemplate)
		check(hp.AtName("body_template"), h.BodyTemplate)
		check(hp.AtName("success_predicate"), h.SuccessPredicate)
	}
	if d.CredentialPolicy != nil {
		check(root.AtName("credential_policy").AtName("reauth_triggers"), d.CredentialPolicy.ReauthTriggers)
	}
	if d.AuditPolicy != nil {
		check(root.AtName("audit_policy").AtName("event_catalog"), d.AuditPolicy.EventCatalog)
	}
	return diags
}
