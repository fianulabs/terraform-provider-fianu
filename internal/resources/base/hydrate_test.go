// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package base

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A deploy whose content hash matches returns action="skipped" with an empty
// EntityID. If Hydrate blanked the UUID on that response, Delete would find
// `uuid == ""`, return early, and silently leak the entity server-side — the
// failure the gate resource had to guard against locally.
func TestHydrate_PreservesUUIDOnSparseResponse(t *testing.T) {
	m := &EnvelopeModel{UUID: types.StringValue("existing-uuid")}

	diags := m.Hydrate(context.Background(), EntityEnvelope{
		EntityType: "control",
		EntityID:   "", // sparse "skipped" deploy metadata
		Path:       "some.control",
		Name:       "Some Control",
	})
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if got := m.UUID.ValueString(); got != "existing-uuid" {
		t.Errorf("UUID = %q, want the prior value preserved", got)
	}
}

// A server-supplied EntityID always wins — this is the normal path and must
// not be shadowed by the preservation rule above.
func TestHydrate_ServerUUIDWins(t *testing.T) {
	m := &EnvelopeModel{UUID: types.StringValue("stale-uuid")}

	diags := m.Hydrate(context.Background(), EntityEnvelope{
		EntityType: "control",
		EntityID:   "fresh-uuid",
		Path:       "some.control",
	})
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if got := m.UUID.ValueString(); got != "fresh-uuid" {
		t.Errorf("UUID = %q, want fresh-uuid", got)
	}
}

// On Create the model's UUID is unknown. It must be resolved to a known value
// even when the server returned nothing, or apply fails with "provider
// produced inconsistent result after apply".
func TestHydrate_UnknownUUIDResolvesToKnown(t *testing.T) {
	m := &EnvelopeModel{UUID: types.StringUnknown()}

	diags := m.Hydrate(context.Background(), EntityEnvelope{
		EntityType: "control",
		EntityID:   "",
		Path:       "some.control",
	})
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if m.UUID.IsUnknown() {
		t.Error("UUID is still unknown; apply would reject the state")
	}
	if got := m.UUID.ValueString(); got != "" {
		t.Errorf("UUID = %q, want empty", got)
	}
}
