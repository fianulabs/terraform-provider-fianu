# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Terraform provider (`registry.terraform.io/fianulabs/fianu`) that manages Fianu compliance entities — controls, gates, policies, environments, targets, collections — declaratively. Built on `terraform-plugin-framework` v1.19+ over plugin protocol v6. The provider is the Terraform sibling of `fianu console deploy`: both paths produce identical entities on the server.

v0.1 ships `fianu_control` (resource) and `fianu_control_test` (action) only. Remaining entities are stubbed in the README roadmap and will reuse the shared `internal/resources/base` envelope.

## Common commands

```bash
# Build/install the provider into $GOPATH/bin
go install .

# Run all unit + acceptance tests (acceptance tests need TF_ACC=1)
TF_ACC=1 go test ./...

# Run a single test
TF_ACC=1 go test ./internal/resources/control -run TestAccFianuControl_FullSpec -v

# Acceptance tests only (action tests require terraform CLI ≥ 1.14 on PATH)
TF_ACC=1 go test ./internal/actions/...

# Generate Terraform Registry docs from schema + examples/
go generate ./...
```

There is no Makefile and no lint config in the repo; rely on `go vet ./...` and `go test ./...`.

### Local provider override

Acceptance tests use `httptest` and don't need a real CLI override. For manual testing against the binary, point `~/.terraformrc` at `$GOPATH/bin` (see README "Development" section).

## Architecture

### Layered request flow

```
HCL → terraform-plugin-framework → internal/resources/<entity> → fianu.Client (SDK) → Fianu Console
```

The provider does not speak HTTP directly. All wire traffic goes through `github.com/fianulabs/core/v2/external/pkg/clients/fianu` (the same SDK the `fianu` CLI uses). Deploys hit `POST /entities/artifacts/deploy`; tests hit `POST /entities/artifacts/test`; reads hit `GET /<entity_type>s/<key>`; deletes hit `DELETE /archive/<type>/<uuid>`. The SDK base64-encodes the JSON-marshaled entity into the `X-Fianu-Raw-Content` header — that's the wire-format difference from the CLI (which tars and multipart-POSTs).

### Shared envelope (`internal/resources/base/`)

Every Fianu entity has the same envelope fields: `id` (composite `<entity_type>/<entity_key>`), `uuid`, `path`, `name`, `metadata`, `version`, `parents`, `children`. The base package owns:

- **`envelope.go`** — schema attributes shared across every entity-style resource, plus the `EnvelopeModel` struct that per-resource models embed. Plan-modifier rules are load-bearing: `path` is `RequiresReplace`, computed fields use `UseStateForUnknown` *except* `version.status`/`version.state`, which intentionally surface server-side workflow drift on each plan.
- **`envelope_builders.go`** — generic `EnvelopeFromStandardEntity[T]` and `EnvelopeFromDeployMetadata` helpers; per-resource hydrate paths feed these to `EnvelopeModel.Hydrate`.
- **`identifier.go`** — `FormatID`/`ParseID` for the `<entity_type>/<entity_key>` composite resource ID. `ParseID` accepts a bare key for backward-compat imports but rejects a wrong type prefix.
- **`identity.go`** — Structured Resource Identity schema (Terraform 1.12+, framework 1.15+), the basis for `import { identity = {...} }` blocks.

When adding a new entity (gate, policy, environment, target, collection): create `internal/resources/<entity>/`, embed `base.EnvelopeModel`, call `base.EnvelopeAttributes()` in `Schema`, build the wire entity via the SDK's `fianu.New<X>Builder`, and use `base.EnvelopeFromDeployMetadata` + `base.EnvelopeFromStandardEntity` for hydration.

### `fianu_control` resource (`internal/resources/control/`)

The schema mirrors the on-disk control package format (`spec.yaml` + `rule.rego` + `detail.py` + ...). `resource.go` owns CRUD + Schema; `evaluation.go`/`relations.go`/`assets.go`/`measures.go` factor out each `detail` subsection's schema and HCL-model→entity translation. Notable contracts:

- `buildEntity` delegates to `fianu.NewControlBuilder` — the SDK builder is the single source of truth for `*fianu_entities.Control` construction, shared with `pkg/entities_files/control_deployer.go` server-side.
- Idempotency: the server hashes the entity and returns `action: "skipped"` when content matches. `hydrateFromControl` only reads back the envelope and the `ControlInfo` trio (`full_name`/`display_key`/`description`) — richer Detail sections stay user-authored to avoid drift from server-side canonicalisation/ordering.
- `evaluation[].content` is the byte-for-byte payload that ends up in the deployed entity. Tests assert exact round-trip (`TestAccFianuControl_EvaluationContent_RoundTrips`). `file("${path.module}/rule.rego")` works because Terraform resolves `file()` before the provider sees the value.
- `Results` is exposed as a typed nested attribute (`pass`/`fail`/...) for plan-time validation, but converted to the server's `map[string]bool` shape using `entities.ResultKey*` typed constants in `toResults`. Magic strings for result keys live nowhere else.

### `fianu_control_test` action (`internal/actions/control_test/`)

A Terraform **Action** (framework v1.16+, CLI v1.14+) — imperative, run-on-demand, no state. Same wire endpoint as `fianu console test controls`. Two invocation modes:

1. `terraform action fianu_control_test.foo` — manual.
2. `resource ... { lifecycle { action_trigger { events = [after_create, after_update]; actions = [...] } } }` — runs after every create/update of an associated resource.

The action's `evaluation` schema mirrors `fianu_control.detail.evaluation` so users share cases via `locals` between resource and action. `Invoke` decodes the JUnit-shaped report and streams one `InvokeProgressEvent` per test case; any failure surfaces as a diagnostic so apply exits non-zero. `invokeWithConfig` is the testable seam — bypasses `tfsdk.Config` plumbing.

### Provider config & auth (`internal/provider/provider.go`)

Auth precedence inside `buildAuthenticator`: explicit `token` (or `FIANU_TOKEN`) → OIDC client-credentials (`client_id`/`client_secret`/`token_url`, with `FIANU_*` env-var fallback via `stringOrEnv`). Anything else returns `errMissingCredentials`. The SDK's default permission-token mode is **not** wired up here — that mode is reserved for in-cluster service-to-service callers and would silently fail against the public API gateway. The configured `*fianu.Client` is broadcast to `ResourceData`/`DataSourceData`/`EphemeralResourceData`/`ActionData` so any extension surface can consume it.

### Acceptance tests use an httptest stub, not a real server

`internal/resources/control/resource_test.go::newConsoleStub` stands up a single `httptest.Server` impersonating Console. The stub:

- Decodes `X-Fianu-Raw-Content` (base64 JSON) on every deploy/test call into a `*fianu_entities.Control` and stores it on the stub via `atomic.Value`, so tests assert on the wire payload directly (not just HCL-side state).
- Mimics the real idempotency gate: first deploy returns `action="created"`, repeats with the same `X-Fianu-CI-System-Hash` header return `action="skipped"`.
- Echoes the deployed entity back on `GET /controls/<key>` so Read doesn't drift against user HCL.

Pattern to follow when adding new entity tests: extend `newConsoleStub` with the new routes rather than spinning up a second server.

## SDK dependency

The provider depends on `github.com/fianulabs/core/v2` (currently `v2.16.56`). All SDK types/builders live under `external/`: `external/db/types/fianu/entities`, `external/pkg/clients/fianu`, `external/transport/http/v1`, `external/pkg/variables` (constants like `XFianuRawContent`, `CRRule`, `EntityTypeControl`). The README documents a `replace` directive workflow for sibling-checkout development; v0.1 currently consumes the SDK as a tagged module (no `replace` in `go.mod`).

`GOPRIVATE=github.com/fianulabs` is required to fetch the module locally and in CI.

## Release

Tag push (`v*`) → `.github/workflows/pipeline.yaml` runs on the `fianu-ubuntu-large` self-hosted runner → `fianulabs/tools/integration/setup/go@v2` wires private-module auth → GPG key imported + sanity-signed (fast-fail before cross-compile) → `goreleaser release --clean` publishes zips/SHA256SUMS/signature/manifest to a GitHub Release per `.goreleaser.yml` (15 OS/arch combos).

`main.version` is overridden via `-ldflags` at build time so the binary reports the correct semver in the SDK User-Agent.

## Examples directory

`examples/resources/fianu_control/{sast_checkmarx,unit_tests_pytest,container_scan_wiz}/` are the three vendored production controls — each ships standalone `rule.rego`/`detail.py`/`display.py`/`rule_test.rego` plus `input/` and `data/` fixtures so users can copy-paste-modify. The README's "How this maps to fianu console deploy" table is the canonical reference for the on-disk-package ↔ HCL mapping.
