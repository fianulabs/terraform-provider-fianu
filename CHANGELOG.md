# Changelog

All notable changes to `terraform-provider-fianu` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
- `fianu_gate` Read no longer evicts the gate from state after a successful
  apply. The nested policy is now deployed at the same entity_key as the
  gate itself (was `<gate.path>.policy` — wrong, the policy and gate share
  a path under different entity_type namespaces) and Read refreshes
  `policy_uuid` via a follow-up `FetchPolicy` so Delete still has a valid
  UUID to archive.
- `fianu_policy` and `fianu_gate` (nested policy + pod `matching` scopes):
  criteria expressions now run through `cel.ParseExpression` provider-side,
  populating `ExprSource` with the canonical CEL form (with `$` prefixes
  and `.(type)` casts) and `ExprDisplay` with the raw user form. Without
  pre-parsing, the server's `validateCELExpressions` runs
  `cel.CompileExpression` on the raw form and 400s with
  `"invalid criteria. must be a valid cel expression"`. Matches what the
  legacy YAML deploy path does in `core/external/db/types/fianu/entities/policy.go:818-843`.
- `fianu_gate`: nested policy's `Control.Type` set to `"gate"` so the
  server's policy resolver queries the gate table (was nil → defaulted to
  `"control"` → `400 "failed to resolve control"`).

### Added
- `fianu_policy.detail.assets` and `fianu_gate.detail.policy.assets`: list
  of abstract asset-type paths (e.g., `["repository"]`). Required by the
  server's `PolicyIsValid` which 400s with
  `"at least one assigned asset is required"` when `Detail.Assets` is
  empty. When omitted but `override.asset.types` is set, the provider
  auto-derives the list from override — same paths encode the same thing.

### Added
- `fianu_gate` resource for managing Fianu Gate entities. Gates are
  `entities.Control` with `type=gate`; the server force-fills evaluation,
  policy template, relations, and assets via `applyGateDefaults`, so the
  HCL surface only exposes the user-authored slice: identity, operational
  config, environment bindings, an inline `policy` block (deployed as a
  separate `entities.Policy` targeting the gate), and `pods` (pipeline
  automation rules deployed via `SetEntityPod` with `pod_type =
  "gate_check_rule"`). Pods support default protection level plus scoped
  CEL `matching` overrides for per-environment enforcement.
- `fianu_policy` resource for managing Fianu Policy entities. Supports the
  policy type (standard/exception/target), control reference, variations
  (with per-variation effect, priority, locked flag, and JSON-encoded metric
  overrides), and asset-scope override. Reads use the unified
  `entities.Policy` SDK shape; deletes hit
  `DELETE /api/entities/archive/policy/:uuid`.

## [0.1.0] - 2026-05-13

Initial public release.

### Added

- `fianu_control` resource — full-fidelity schema mirroring the on-disk control
  package format (`spec.yaml` + `rule.rego` + `detail.py` + `display.py` +
  `rule_test.rego` + `input/` + `data/`). Wire format matches `fianu console
  deploy` and honours the same SHA256 content-hash idempotency gate.
- `fianu_control_test` action — parity with `fianu console test controls`.
  Runs rego rules against `input`/`data` fixtures via
  `POST /entities/artifacts/test`. Streams JUnit-shaped progress events;
  failures surface as apply errors.
- Structured Resource Identity (Terraform 1.12+, framework 1.15+) so
  `import { identity = {...} }` blocks work alongside legacy string IDs.
- OIDC client-credentials and static bearer token authentication, with
  `FIANU_*` env var fallback.
- GoReleaser pipeline producing signed zips, SHA256SUMS, manifest, and
  signature for 15 OS/arch combinations.
- Three vendored production controls under `examples/resources/fianu_control/`
  (`sast_checkmarx`, `unit_tests_pytest`, `container_scan_wiz`).

[Unreleased]: https://github.com/fianulabs/terraform-provider-fianu/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/fianulabs/terraform-provider-fianu/releases/tag/v0.1.0
