# Terraform Provider for Fianu

Manage Fianu compliance entities (controls, gates, policies, environments,
targets, collections) declaratively from Terraform.

> **Status:** v0.1 — early development. Schema and resource shapes will change
> before v1.0. Follow [`CHANGELOG.md`](./CHANGELOG.md) once it lands.

## What v0.1 ships

| Resource          | Status        |
| ----------------- | ------------- |
| `fianu_control`   | ✅ Available  |
| `fianu_gate`      | ⏳ v0.1.x     |
| `fianu_policy`    | ⏳ v0.1.x     |
| `fianu_environment` | ⏳ v0.1.x   |
| `fianu_target`    | ⏳ v0.1.x     |
| `fianu_collection` | ⏳ v0.1.x    |

## Authentication

The provider supports two auth methods. OIDC client-credentials is preferred;
static bearer is a fallback for CI service accounts that already hold a
long-lived token.

### OIDC client-credentials (recommended)

```hcl
provider "fianu" {
  host          = "https://console.fianu.io"
  client_id     = var.fianu_client_id
  client_secret = var.fianu_client_secret
  token_url     = "https://auth.fianu.io/oauth/token"
}
```

Falls back to `FIANU_HOST`, `FIANU_CLIENT_ID`, `FIANU_CLIENT_SECRET`,
`FIANU_TOKEN_URL` env vars when the matching attributes are unset.

Token caching and refresh are handled by `golang.org/x/oauth2/clientcredentials`.

### Static bearer token

```hcl
provider "fianu" {
  host  = "https://console.fianu.io"
  token = var.fianu_token
}
```

Or via `FIANU_TOKEN`.

## Usage

```hcl
resource "fianu_control" "payment_service_sast" {
  path = "payment-service-sast"
  name = "Payment Service SAST"

  detail = {
    full_name   = "Payment Service Static Analysis"
    display_key = "PSSAST"
    description = "Static analysis gates for the payment service repository."
  }
}
```

The composite resource ID is `<entity_type>/<entity_key>` — see
[`examples/resources/fianu_control/import.sh`](./examples/resources/fianu_control/import.sh)
for the import command.

## Compatibility

| Provider | Fianu Console API | Terraform |
| -------- | ----------------- | --------- |
| 0.1.x    | 1.x               | ≥ 1.12    |

The provider implements [Resource Identity](https://developer.hashicorp.com/terraform/plugin/framework/resources/identity)
(GA in Terraform 1.12 / framework 1.15) so import blocks support both legacy
ID strings and structured identity.

## Development

```bash
# Build the provider locally
go install .

# Wire it into ~/.terraformrc for local testing
cat <<EOF > ~/.terraformrc
provider_installation {
  dev_overrides {
    "fianulabs/fianu" = "$(go env GOPATH)/bin"
  }
  direct {}
}
EOF

# Run unit + acceptance tests against an httptest stub
TF_ACC=1 go test ./...
```

The provider depends on the SDK in
`github.com/fianulabs/core/v2/external/pkg/clients/fianu`. During v0
development the dependency is resolved via a `replace` directive in
[`go.mod`](./go.mod) pointing at a sibling checkout of the core monorepo.
Once the SDK is published as a tagged module, the replace will be removed.

## License

TBD.
