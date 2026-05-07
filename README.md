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

## Authoring real controls

The provider mirrors the on-disk control package format used by `fianu console
deploy` — see the [official-controls repo](https://github.com/fianulabs/official-controls)
for production examples. Three real controls are vendored as HCL fixtures so
you can copy-paste-modify:

| HCL example | Source on-disk control |
| ----------- | ---------------------- |
| [`examples/resources/fianu_control/sast_checkmarx/`](./examples/resources/fianu_control/sast_checkmarx/) | `official-controls/envs/dev/controls/sast/checkmarx.sast.vulnerabilities/` |
| [`examples/resources/fianu_control/unit_tests_pytest/`](./examples/resources/fianu_control/unit_tests_pytest/) | `official-controls/envs/dev/controls/unit.tests/testing.unit.pytest.results/` |
| [`examples/resources/fianu_control/container_scan_wiz/`](./examples/resources/fianu_control/container_scan_wiz/) | `official-controls/envs/dev/controls/container.scan/wiz.containerscan.vulnerabilities/` |

Each example keeps `rule.rego`, `detail.py`, `display.py`, and `rule_test.rego`
as standalone files (so syntax highlighting and linters keep working) and
loads them via `file()`:

```hcl
detail = {
  evaluation = [
    { type = "rule", engine = "opa", label = "rule.rego", content = file("${path.module}/rule.rego") },
    { type = "detail",                label = "detail.py", content = file("${path.module}/detail.py") },
  ]
}
```

The spec.yaml fields (`relations`, `assets`, `policy_template.measures`,
`results`, `documentation`, `config`) are first-class HCL — every section
is typed, validated at plan time, and diffs cleanly.

### How this maps to `fianu console deploy`

```
on-disk package                         HCL resource
───────────────                         ────────────
my-control/                             resource "fianu_control" "x" {
  spec.yaml          ◄───────────►        path / name / detail.{full_name,…}
  rule.rego          ◄───────────►        detail.evaluation[type="rule"]
  detail.py          ◄───────────►        detail.evaluation[type="detail"]
  display.py         ◄───────────►        detail.evaluation[type="display"]
  rule_test.rego     ◄───────────►        detail.evaluation[type="rule_test"]
  input/*.json       ◄───────────►        detail.evaluation[type="input"]
  data/*.json        ◄───────────►        detail.evaluation[type="data"]
                                        }
```

Both deploy paths funnel through the same server endpoint
(`POST /entities/artifacts/deploy`) and the same `pkg/entities_files/control_deployer.go`
code, so they produce identical `Control` rows and honour the same SHA256
content-hash idempotency gate. The only difference is the wire format:

- **CLI**: tars the directory, multipart-POSTs `payload` (JSON metadata) +
  `file` (binary archive). Server's `BuildControlFromFiles` extracts and
  builds the entity.
- **Provider**: builds `*entities.Control` in Go, JSON-marshals it,
  base64-encodes into the `X-Fianu-Raw-Content` header, and POSTs.

A second `terraform apply` with no HCL changes returns `action: "skipped"`
from the server and zero diff in Terraform — same idempotency story as
re-running `fianu console deploy` against an unchanged directory.

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
