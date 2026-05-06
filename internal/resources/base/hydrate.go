package base

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// EntityEnvelope is the minimal shape every Fianu entity exposes — UUID,
// path, name, plus the StandardVersion sub-fields. Per-resource hydrate
// helpers populate one of these and call EnvelopeModel.Hydrate.
//
// Defining the contract here (instead of importing entities.StandardEntity)
// keeps the base package free of any cross-package coupling and makes it
// trivial to fake in unit tests.
type EntityEnvelope struct {
	EntityType       string
	EntityID         string
	Path             string
	Name             string
	VersionSemantic  string
	VersionUUID      string
	VersionStatus    string
	VersionState     string
	VersionTimestamp time.Time
	Metadata         map[string]string
	Parents          []string
	Children         []string
}

// Hydrate copies envelope fields from the server response into the model. It
// preserves user-authored fields (path, name) — the server normally echoes
// them back unchanged, but in case the server canonicalises a path we let
// the server win (state stays in sync with what was actually persisted).
func (m *EnvelopeModel) Hydrate(ctx context.Context, env EntityEnvelope) diag.Diagnostics {
	var diags diag.Diagnostics

	m.ID = types.StringValue(FormatID(env.EntityType, env.Path))
	m.UUID = types.StringValue(env.EntityID)
	m.Path = types.StringValue(env.Path)
	m.Name = types.StringValue(env.Name)

	versionObj, d := types.ObjectValue(versionAttrTypeMap(), map[string]attr.Value{
		"semantic":  types.StringValue(env.VersionSemantic),
		"uuid":      types.StringValue(env.VersionUUID),
		"status":    types.StringValue(env.VersionStatus),
		"state":     types.StringValue(env.VersionState),
		"timestamp": types.StringValue(env.VersionTimestamp.UTC().Format(time.RFC3339)),
	})
	diags.Append(d...)
	m.Version = versionObj

	metadataMap, d := types.MapValueFrom(ctx, types.StringType, env.Metadata)
	diags.Append(d...)
	m.Metadata = metadataMap

	parentsList, d := types.ListValueFrom(ctx, types.StringType, env.Parents)
	diags.Append(d...)
	m.Parents = parentsList

	childrenList, d := types.ListValueFrom(ctx, types.StringType, env.Children)
	diags.Append(d...)
	m.Children = childrenList

	return diags
}

// versionAttrTypeMap returns the attr.Type map matching the version
// SingleNestedAttribute. Kept private so the schema definition in envelope.go
// stays the source of truth and Hydrate doesn't drift.
func versionAttrTypeMap() map[string]attr.Type {
	return map[string]attr.Type{
		"semantic":  types.StringType,
		"uuid":      types.StringType,
		"status":    types.StringType,
		"state":     types.StringType,
		"timestamp": types.StringType,
	}
}

// emptyVersion returns a typed null Object for the version attribute. Useful
// for tests that build an EnvelopeModel manually.
func emptyVersion() basetypes.ObjectValue {
	return types.ObjectNull(versionAttrTypeMap())
}

var _ = emptyVersion // exported via tests when needed
