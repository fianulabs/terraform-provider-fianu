package base

import (
	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	transportv1 "github.com/fianulabs/core/v2/external/transport/http/v1"
)

// EnvelopeFromStandardEntity reads envelope fields off any concrete
// StandardEntity[T] entity. Each per-resource hydrate path (control, plus
// future gate/policy/environment/target/collection) calls this to populate
// the shared EntityEnvelope without re-stating the field set.
//
// Generic over T so any entity implementation can pass its embedded
// *StandardEntity[T] directly.
func EnvelopeFromStandardEntity[T any](entityType string, e *fianu_entities.StandardEntity[T]) EntityEnvelope {
	if e == nil {
		return EntityEnvelope{EntityType: entityType}
	}
	return EntityEnvelope{
		EntityType:       entityType,
		EntityID:         e.UUID,
		Path:             e.Path,
		Name:             e.Name,
		VersionSemantic:  e.Version.Semantic,
		VersionUUID:      e.Version.UUID,
		VersionStatus:    string(e.Version.Status),
		VersionState:     string(e.Version.State),
		VersionTimestamp: e.Version.Timestamp,
		Metadata:         map[string]string{},
		Parents:          []string{},
		Children:         []string{},
	}
}

// EnvelopeFromDeployMetadata builds an envelope from the server's deploy
// response metadata. Used by Create/Update where the only signal is the
// metadata payload (action="skipped" responses don't carry a full entity).
//
// fallbackPath / fallbackName let callers preserve user-authored values
// when the server returns a sparse "skipped" response.
func EnvelopeFromDeployMetadata(entityType string, m *transportv1.DeploymentMetadata, fallbackPath, fallbackName string) EntityEnvelope {
	if m == nil {
		return EntityEnvelope{
			EntityType: entityType,
			Path:       fallbackPath,
			Name:       fallbackName,
			Metadata:   map[string]string{},
			Parents:    []string{},
			Children:   []string{},
		}
	}
	path := m.Path
	if path == "" {
		path = fallbackPath
	}
	name := m.Name
	if name == "" {
		name = fallbackName
	}
	return EntityEnvelope{
		EntityType:      entityType,
		EntityID:        m.EntityID,
		Path:            path,
		Name:            name,
		VersionSemantic: m.Version,
		Metadata:        map[string]string{},
		Parents:         []string{},
		Children:        []string{},
	}
}
