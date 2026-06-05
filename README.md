# Terraform Provider for Fianu

[![Release](https://img.shields.io/github/v/release/fianulabs/terraform-provider-fianu?sort=semver)](https://github.com/fianulabs/terraform-provider-fianu/releases)
[![Registry](https://img.shields.io/badge/registry-fianulabs%2Ffianu-623CE4?logo=terraform)](https://registry.terraform.io/providers/fianulabs/fianu/latest)
[![Go Reference](https://pkg.go.dev/badge/github.com/fianulabs/terraform-provider-fianu.svg)](https://pkg.go.dev/github.com/fianulabs/terraform-provider-fianu)
[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-blue.svg)](./LICENSE)
[![CI](https://github.com/fianulabs/terraform-provider-fianu/actions/workflows/ci.yaml/badge.svg)](https://github.com/fianulabs/terraform-provider-fianu/actions/workflows/ci.yaml)

Manage Fianu compliance entities (controls, gates, policies, environments,
targets, collections) declaratively from Terraform.

> **Status:** v0.1 — early development. Schema and resource shapes will change
> before v1.0. See [`CHANGELOG.md`](./CHANGELOG.md) for release notes.

## What v0.1 ships

| Resource          | Status        |
| ----------------- | ------------- |
| `fianu_control`   | ✅ Available  |
| `fianu_gate`      | ✅ Available  |
| `fianu_policy`    | ✅ Available  |
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
  host          = "https://app.fianu.io"
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
  host  = "https://app.fianu.io"
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

### Testing controls — `fianu_control_test` action

The provider exposes a [Terraform Action](https://developer.hashicorp.com/terraform/plugin/framework/actions)
(framework v1.16+, Terraform CLI v1.14+) that runs a control's rego rules
against its `input`/`data` fixtures via `POST /entities/artifacts/test`.
Same wire contract as `fianu console test controls ./...`; same JUnit-shaped
report streamed back as per-case progress events.

```hcl
locals {
  evaluation = [
    { type = "rule",   engine = "opa", content = file("${path.module}/rule.rego") },
    { type = "detail",                 content = file("${path.module}/detail.py") },
    { type = "input",  content = file("${path.module}/input/occ_case_1.json") },
    { type = "data",   content = file("${path.module}/data/policy_case_1.json") },
  ]
}

resource "fianu_control" "sast" {
  path = "checkmarx.sast.vulnerabilities"
  name = "SAST"
  detail = {
    full_name   = "Static Asset Security Analysis"
    display_key = "CHXST"
    evaluation  = local.evaluation   # rules + fixtures shared with the action
    # …rest of detail…
  }

  # Run rego tests after every create/update.
  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.fianu_control_test.sast]
    }
  }
}

action "fianu_control_test" "sast" {
  config {
    path       = "checkmarx.sast.vulnerabilities"
    name       = "SAST"
    evaluation = local.evaluation
  }
}
```

Run on demand:

```bash
terraform apply -invoke=action.fianu_control_test.sast
```

Or watch it run as part of `terraform apply` — the `lifecycle.action_trigger`
block above invokes it after every create/update of the resource. Failed
test cases surface as apply errors; successful runs stream `✓ occ_case_1`
progress events to the CLI.

All three vendored examples (`sast_checkmarx`, `unit_tests_pytest`,
`container_scan_wiz`) now ship with their `input/`/`data/` fixtures and
matching action blocks — copy any of them as a complete working starting
point.

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
`github.com/fianulabs/core/v2/external/pkg/clients/fianu`, consumed as a tagged
module. Sibling-checkout development (via a temporary `replace` directive in
[`go.mod`](./go.mod)) is supported but not required.

`GOPRIVATE=github.com/fianulabs` is required to fetch the module locally and
in CI.

## Contributing

See [`CONTRIBUTING.md`](./CONTRIBUTING.md) for the dev workflow, test commands,
and the project's commit + PR conventions. Security issues should be reported
per [`SECURITY.md`](./SECURITY.md).

## License

Released under the [Mozilla Public License 2.0](./LICENSE) — the standard
license for HashiCorp-ecosystem Terraform providers.
