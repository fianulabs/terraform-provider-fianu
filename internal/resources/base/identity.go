package base

import "github.com/hashicorp/terraform-plugin-framework/resource/identityschema"

// EnvelopeIdentitySchema returns the structured Resource Identity (TF 1.12+,
// framework 1.15+) shared by every entity-style resource. Identity is what
// makes `import { identity = {...} }` blocks unambiguous and survives
// state-version migrations more cleanly than the legacy `id` string.
//
// `entity_type` and `entity_key` together uniquely identify any Fianu entity
// in a tenant; `uuid` is optional for advanced pinning to a specific entity
// version on import.
func EnvelopeIdentitySchema() identityschema.Schema {
	return identityschema.Schema{
		Attributes: map[string]identityschema.Attribute{
			"entity_type": identityschema.StringAttribute{
				RequiredForImport: true,
				Description:       "Entity type (e.g., `control`, `policy`, `environment`).",
			},
			"entity_key": identityschema.StringAttribute{
				RequiredForImport: true,
				Description:       "Stable human-readable entity key (slug).",
			},
			"uuid": identityschema.StringAttribute{
				OptionalForImport: true,
				Description:       "Server-generated UUID. Optional — only required when pinning to a specific entity instance.",
			},
		},
	}
}
