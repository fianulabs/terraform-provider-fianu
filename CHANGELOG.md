# Changelog

All notable changes to `terraform-provider-fianu` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
