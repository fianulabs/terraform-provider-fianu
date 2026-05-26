// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package gate

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	sdk "github.com/fianulabs/core/v2/external/pkg/sdk/v2"
	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// variationModel is one entry in a gate's nested policy variations.
//
// Unlike `fianu_policy` variations — which carry a free-form `policy`
// JSONB of metric overrides — a gate's policy template is fixed by the
// server to a single `controls` measure. The wire shape the server expects
// in `policy_rule_sets.policy` is therefore `{<label>: <entity_uuid>}`
// per required entity; anything else corrupts the row and breaks the
// gate-children lateral join in
// core/external/db/controls/v1/enrichment.go.
//
// The provider models that as two explicit lists — required_controls and
// required_gates — and resolves each path/UUID to the entity's UUID at
// apply time. That keeps the user-facing surface aligned with the "Gate
// Requirements" dialog (Required Controls + Required Gates) and makes
// the gate→child wiring impossible to author incorrectly.
type variationModel struct {
	Effect   types.String   `tfsdk:"effect"`
	Priority types.Int64    `tfsdk:"priority"`
	Locked   types.Bool     `tfsdk:"locked"`
	Criteria *criteriaModel `tfsdk:"criteria"`
	// RequiredControls is the list of fianu_control entities this variation
	// gates on. Each entry is either a control path (e.g.,
	// "terraform.example.iac.scan") or an entity UUID. Paths are resolved
	// to UUIDs at apply via FetchControl.
	RequiredControls []types.String `tfsdk:"required_controls"`
	// RequiredGates is the list of fianu_gate entities this variation
	// depends on. Each entry is either a gate path or an entity UUID.
	// Paths are resolved via FetchGate.
	RequiredGates []types.String `tfsdk:"required_gates"`
}

func variationsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "Variations of the gate's policy. Each variation pairs an effect (apply or exempt) with the set of controls and gates that must pass for the gate to pass when the variation's `criteria` matches. The server evaluates variations in priority order; the first matching variation wins. Leave `criteria` unset for an unconditional requirement set.",
		Required:            true,
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"effect": schema.StringAttribute{
					MarkdownDescription: "What this variation does. `apply` enforces the required_controls/required_gates; `exempt` skips the gate entirely for matching assets. Defaults to `apply` when omitted.",
					Optional:            true,
					Validators: []validator.String{
						stringvalidator.OneOf(
							string(fianu_entities.PolicyEffectApply),
							string(fianu_entities.PolicyEffectExempt),
						),
					},
				},
				"priority": schema.Int64Attribute{
					MarkdownDescription: "Evaluation priority. Lower numbers run first. Defaults to 0.",
					Optional:            true,
				},
				"locked": schema.BoolAttribute{
					MarkdownDescription: "When true, prevents downstream tenants from overriding this variation. Defaults to false.",
					Optional:            true,
				},
				"required_controls": schema.ListAttribute{
					MarkdownDescription: "Controls that must pass for this variation to pass. Each entry is a control path (e.g., `terraform.example.iac.scan`) or an entity UUID. Paths are resolved to UUIDs at apply via the Console API.",
					Optional:            true,
					ElementType:         types.StringType,
				},
				"required_gates": schema.ListAttribute{
					MarkdownDescription: "Other gates that must pass for this variation to pass. Each entry is a gate path or entity UUID. Use this to chain gates (e.g., \"unit tests must pass before the security gate runs\").",
					Optional:            true,
					ElementType:         types.StringType,
				},
				"criteria": criteriaAttribute(),
			},
		},
	}
}

// buildVariations translates the HCL variations into the wire shape the
// server expects in `policy_rule_sets.policy` — a `{<label>: <uuid>}`
// map per variation. Each required_controls entry is resolved via
// FetchControl; each required_gates entry via FetchGate. The label is the
// user-supplied path/UUID (kept stable so successive applies are
// idempotent); the value is the resolved entity UUID.
//
// Resolution failures are returned as diagnostics — callers should
// short-circuit deploys when buildVariations returns errors so a partial
// (silently-broken) policy never lands on the server.
func buildVariations(ctx context.Context, client *sdk.Client, in []variationModel) ([]fianu_entities.PolicyVariation, diag.Diagnostics) {
	var diags diag.Diagnostics
	out := make([]fianu_entities.PolicyVariation, len(in))
	for i, v := range in {
		policy := fianu_entities.PolicyVariationDetail{}

		for _, ref := range v.RequiredControls {
			if ref.IsNull() || ref.IsUnknown() {
				continue
			}
			label := ref.ValueString()
			if label == "" {
				continue
			}
			id, rdiags := resolveControlUUID(ctx, client, label)
			diags.Append(rdiags...)
			if rdiags.HasError() {
				continue
			}
			policy[label] = id
		}

		for _, ref := range v.RequiredGates {
			if ref.IsNull() || ref.IsUnknown() {
				continue
			}
			label := ref.ValueString()
			if label == "" {
				continue
			}
			id, rdiags := resolveGateUUID(ctx, client, label)
			diags.Append(rdiags...)
			if rdiags.HasError() {
				continue
			}
			policy[label] = id
		}

		out[i] = fianu_entities.PolicyVariation{
			PolicyEffect: fianu_entities.PolicyEffect(v.Effect.ValueString()),
			Priority:     int(v.Priority.ValueInt64()),
			Locked:       v.Locked.ValueBool(),
			Policy:       policy,
			Criteria:     v.Criteria.toEntity(),
		}
	}
	return out, diags
}

// resolveControlUUID accepts either a UUID-formatted string (returned
// untouched) or an entity path (fetched + UUID extracted). The server's
// gate-children CTE casts each map value to ::uuid, so anything we put
// on the wire MUST parse as a UUID.
func resolveControlUUID(ctx context.Context, client *sdk.Client, ref string) (string, diag.Diagnostics) {
	var diags diag.Diagnostics
	if _, err := uuid.Parse(ref); err == nil {
		return ref, diags
	}
	ctrl, err := client.FetchControl(ctx, ref, nil, nil)
	if err != nil {
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			diags.AddError(
				"required_control not found",
				fmt.Sprintf("Control %q does not exist on the Fianu console. Deploy the control before referencing it from a gate.", ref),
			)
			return "", diags
		}
		diags.AddError("resolve required_control failed", fmt.Sprintf("ref=%q: %s", ref, err.Error()))
		return "", diags
	}
	if ctrl == nil || ctrl.UUID == "" {
		diags.AddError("resolve required_control failed", fmt.Sprintf("control %q resolved to an empty entity_id", ref))
		return "", diags
	}
	return ctrl.UUID, diags
}

func resolveGateUUID(ctx context.Context, client *sdk.Client, ref string) (string, diag.Diagnostics) {
	var diags diag.Diagnostics
	if _, err := uuid.Parse(ref); err == nil {
		return ref, diags
	}
	g, err := client.FetchGate(ctx, ref, nil)
	if err != nil {
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			diags.AddError(
				"required_gate not found",
				fmt.Sprintf("Gate %q does not exist on the Fianu console. Deploy the gate before referencing it from another gate.", ref),
			)
			return "", diags
		}
		diags.AddError("resolve required_gate failed", fmt.Sprintf("ref=%q: %s", ref, err.Error()))
		return "", diags
	}
	if g == nil || g.UUID == "" {
		diags.AddError("resolve required_gate failed", fmt.Sprintf("gate %q resolved to an empty entity_id", ref))
		return "", diags
	}
	return g.UUID, diags
}
