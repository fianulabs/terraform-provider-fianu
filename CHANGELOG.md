# Changelog

All notable changes to `terraform-provider-fianu` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Data sources for every entity type: `fianu_collection`, `fianu_control`,
  `fianu_environment`, `fianu_form`, `fianu_gate`, `fianu_index`,
  `fianu_instance`, `fianu_platform`, `fianu_policy`, `fianu_policy_exception`,
  `fianu_report_template`, `fianu_target` and `fianu_tool`. Each looks an
  entity up by `path` and exposes its envelope, most usefully `uuid`.

  This closes the cross-reference gap. Entities reference each other by UUID —
  `fianu_instance.detail.platform_uuid` is the common case — and
  `fianu_platform.jira.uuid` only resolves when the same configuration creates
  the platform. For a platform that predates Terraform, lives in another state
  file, or belongs to another team, the alternatives were a hardcoded UUID
  (opaque, unchecked, and stale the moment the entity is archived and
  recreated) or `terraform_remote_state`.

  Lookup is by `path`, not UUID: the path is the identifier humans author and
  `fianu console deploy` keys on, so path-in/UUID-out is the conversion that
  removes the hardcoding. A lookup that finds nothing is an error rather than
  an empty result — unlike a resource `Read`, where a 404 evicts state, here it
  means the configuration points at something that does not exist, and
  continuing would feed an empty UUID downstream.

  Data sources expose the envelope only, not `detail` — the same rule the
  resources follow for hydration. The pod-backed resources (`fianu_entity_pod`,
  `fianu_notification`) have no data source: pods are rows keyed by
  `(entity_id, pod_type, key)`, not entities, so there is no path to look one
  up by.

- `fianu_tool` resource — an integration that produces the evidence controls
  evaluate. `sources.produces` is what makes a tool's output addressable by a
  control's evaluation input; the server rejects a `consumes` edge with no
  matching active producer.
- `fianu_platform` resource — the integration product instances connect to,
  carrying the operational contract every instance inherits: endpoint defaults,
  health probes, credential rotation, error semantics and audit policy. Enum
  attributes are validated against the server's own constants rather than
  hardcoded lists. `capabilities` (a Fianu-owned catalog) and `instances` (a
  computed count) are deliberately not in the schema. Platform types remain
  Console-managed; reference one by UUID, as `fianu_collection` does for
  domains.

- `fianu_report_template` resource — the layout that composes control
  templates into a full report. `layout_config` is a `jsonencode`-authored JSON
  object, validated at plan time. The entity type on the wire is `template`, so
  the resource `id` and import prefix are `template/<key>`, not
  `report_template/<key>`.

- `fianu_instance` resource — a configured, reachable deployment of a platform.
  `detail.domains` carries the endpoints inline: a domain has no identity
  outside the instance version that declares it, so it is not a child entity
  and does not appear in the entity graph. Domain uuids are server-assigned per
  version and are not settable. Credentials are deliberately out of scope —
  they are secrets with their own rotation lifecycle and their own endpoints,
  and an entity version is the wrong place for either.

- `fianu_form` resource — the reusable questionnaire definition an attestation
  presents. Element `options` are `jsonencode`-authored and validated at plan
  time; `type` is restricted to the five element kinds the server can actually
  build (`dropdown` is in the SQL enum but has no implementation). Element
  order is meaningful — the server derives each element's `code` from its
  position and binds submitted answers to that code. Forms deployed through
  Terraform are published/active: the deployer defaults an unspecified form to
  draft/inactive to match the console's create endpoint, but the form read
  filters to active, so a draft would 404 on its own next refresh.

- `fianu_policy_exception` resource — a policy exception, the narrower waiver
  that overrides a standard policy for the assets its criteria match. Same
  shape as `fianu_policy` (same control binding, same variations); `detail.type`
  is optional and defaults to `exception`. Server-side an exception is the same
  `entities.Policy` handled by the same deployer, but it is stored and read as
  its own entity type, so it is its own resource with its own composite ID
  (`policy_exception/<key>`) and its own read/archive routes.

- `fianu_control`: `detail.template` — the report template section of the
  on-disk control package. `template_content` (a TemplateSpec, wizard or raw
  mode) and `schema_snapshot` are `jsonencode`-authored passthroughs validated
  at plan time; their shapes are owned by the reporting service, so modelling
  them in HCL would pin a schema this provider does not own. Omitting the block
  sends no template at all rather than an empty one, because the server applies
  the section only when it is present.

### Changed

- **BREAKING** — `fianu_policy`: `detail.type` no longer accepts `exception`.
  Use `fianu_policy_exception`. A `fianu_policy` carrying `type = "exception"`
  deployed as a real exception — the server routes on `detail.type`
  (`pkg/entities_files/policy_deployer.go`) — but the resource then read it back
  with `FetchPolicy`, which filters `entityType=policy` and never returns
  exceptions. Every refresh 404'd and silently evicted the resource from state,
  and `terraform destroy` archived through the policy route. Configurations
  using that value were already broken; move them to the new resource and
  re-import with the `policy_exception/` prefix.

### Fixed

- `fianu_policy`, `fianu_collection`, `fianu_environment` and `fianu_target`
  import no longer fails with a value-conversion error. `ImportState` set only
  `path`, leaving `detail` null, which the framework cannot decode into a
  non-pointer detail model — import failed outright with "Received null value,
  however the target type cannot handle null values". All four now pre-populate
  the detail object, matching what `fianu_control` has done since 0.2. None of
  them had an import test; every resource has one now.


## [0.3.0] - 2026-08-08

### Changed

- **BREAKING** — `fianu_gate`: `detail.pods` is replaced by `detail.gate`.
  Fianu Console removed the `gate_check_rule` entity pod; gate rules are now
  native to the gate entity (`entities.ControlDetail.Gate`), versioned with it
  and written in the same deploy call instead of through separate pod API
  round-trips. Each pod becomes an entry in `detail.gate.checks[]`, with the
  pod's `key` becoming the check's `name`. See the migration section in
  `README.md`.
- **BREAKING** — `fianu_gate`: `detail.pod_keys` is removed from state. It
  tracked the pod keys the provider had to reconcile; there are no pods to
  reconcile any more.
- `fianu_gate`: `detail.gate.checks[].completion_action` is now validated
  against the server enum (`post_check_status`, `auto_approve_pr`).

### Added

- `fianu_environment` resource — named deployment stages, with a `matching` CEL
  group deciding which deployment events belong to each. Completes the entity
  roadmap alongside the two below.
- `fianu_target` resource — concrete cloud deployment destinations bound to one
  or more environments. `environments[]` accepts an environment path or UUID;
  paths resolve server-side.
- `fianu_collection` resource — groups controls under a domain. `detail.domain`
  is the parent domain's entity UUID (domains remain Console-managed).
- `fianu_notification` resource — manages a notification config pod on a control
  or gate. Typed schema over all 13 notification buckets, with plan-time
  validation of type, urgency, mode, recipients, and channels derived from the
  SDK's registries rather than hardcoded. Its `rules` block is the same asset
  matcher as `fianu_policy` variation criteria and `fianu_gate` check matching.
- `fianu_entity_pod` resource — the generic form: attaches any pod type to any
  entity with a `jsonencode`-authored `value`. Means new platform pod types work
  without waiting on a provider release.
- `fianu_gate`: `detail.gate.enabled` — the gate's master switch. Omitted means
  off, matching the server default.
- `fianu_gate`: `detail.gate.checks[].gating_sources` — deciding systems that
  must all pass for the check to pass. Defaults to `["fianu"]`.

- Bumped `github.com/fianulabs/core/v2` to `v2.21.20`, which carries the SDK
  methods the three new entity resources need (`FetchEnvironment`,
  `FetchTarget`, `FetchCollection` and their `Archive*` counterparts).

### Fixed

- Entity resources no longer lose their `uuid` when a deploy is skipped. The
  server returns `action="skipped"` with an empty `EntityID` when the content
  hash is unchanged; `base.Hydrate` wrote that empty value straight into state,
  and every `Delete` short-circuits on `uuid == ""` — so the entity was never
  archived. Reachable when a skipped deploy coincided with a failed refetch.
  `Hydrate` now preserves a known UUID and only overwrites it when the server
  supplies one, or when the prior value is null/unknown (Create).
  Affected `fianu_policy`, `fianu_environment`, `fianu_target` and
  `fianu_collection`; `fianu_gate` had a local guard against the same failure.
- `fianu_gate` no longer writes the deprecated `PolicyAssetOverride` shape on
  its inline policy. `detail.policy.override` now folds into `Detail.Assets`,
  which the server expands back into the same override via
  `buildOverrideFromAssets` before resolving scope — identical resolution,
  no deprecated field. No HCL change: `override` and `assets` keep working as
  before, and `override` still supersedes `assets` exactly as the server
  already treated them.
- **`fianu_control_test` never reported a failure.** The action parsed the
  report using JUnit *XML* element names — `testsuites`, `testcase`, `failure`
  — but the server marshals Go structs from
  `external/db/types/fianu/testing/v1.0.0` whose JSON keys are `suites`,
  `tests` and `status`. Every key missed, so the action walked zero cases,
  logged `0/0 cases passed` and exited clean no matter what the tests did.
  Anyone who wired the action into a resource's `lifecycle.action_trigger`
  had a test step that has been green since it was written.
  The action now reads the real shape, counts cases nested under `suites[]`
  *and* those on the report's own `tests[]` (the shape policy validation
  returns), treats `status: "error"` as a failure rather than a pass, surfaces
  the underlying cause from the case's `error` object, and fails when a report
  contains no cases at all — a test step that runs nothing is not a pass.
- `fianu_entity_pod` no longer rejects unknown `pod_type` values on import.
  The schema accepts any pod type on create — that is the point of the generic
  resource — but `ImportState` validated against the pinned SDK's enum, so a
  pod type newer than the provider's SDK could be created but not imported.

## [0.2.3] - 2026-06-08

### Added
- Provider `token` now falls back to a token persisted by `fianu auth login`
  at `~/.fianu/fianu.conf.v1` (override the directory with
  `FIANU_CLI_HOME_DIR`), after `token` and `FIANU_TOKEN`. This is how the
  GitHub OIDC / workload-identity-federation flow hands a token to the
  provider with no static secrets. Documented retroactively — v0.2.3 shipped
  without a changelog entry.

## [0.2.2] - 2026-06-08

### Added
- Plan-time validation of `criteria` shape on `fianu_policy.detail.variations[*].criteria`,
  `fianu_gate.detail.policy.variations[*].criteria`, and
  `fianu_gate.detail.pods[*].matching[*]`. Catches the three error cases the
  server's `PolicyAssetGroup.IsValid` rejects at apply time —
  `expressions` + `indexes` both set, `expressions` without `asset.type`,
  and an entirely empty criteria. Exact parity: the validator imports
  `fianu_entities` and calls `(*PolicyAssetGroup).IsValid()` directly, so
  the server's error message flows through verbatim and any future
  rule changes in core are picked up automatically on SDK bump.
  See `internal/resources/base/criteria_validator.go`.
- Plan-time validation on `fianu_policy` that every variation carries
  `criteria.asset.type`. `fianu_policy` has no top-level `assets`/`override`
  attribute so the server's `allVariationsHaveCriteriaAsset` rule
  (`policy.go::PolicyIsValid`) is the only path that satisfies the
  policy-level binding check — including for indexes-only variations,
  which the per-criteria validator alone doesn't reject.

## [0.2.1] - 2026-06-08

### Fixed
- OIDC client-credentials token requests now include the `audience` form
  parameter. Without it, Auth0 M2M clients whose tenant has no Default
  Audience configured failed at provider init with
  `access_denied: No audience parameter was provided, and no default audience
  has been configured`. Default is `https://fianu.us.auth0.com/api/v2`
  (the production Fianu API audience); override via the new `audience`
  provider attribute or `FIANU_AUDIENCE` env var when running against a
  private deployment. Plumbed through `sdk.WithOIDCAudience` (new in core
  `external/pkg/sdk/v2` ≥ v2.16.108).

## [0.2.0] - 2026-06-08

### Changed (breaking)
- `fianu_gate.detail.policy.variations[]` no longer accepts the free-form
  `policy` JSON map that mirrored `fianu_policy`. A gate's policy template
  is server-fixed to a single `controls` measure whose value is an array
  of child-entity UUIDs, so authoring arbitrary measure overrides
  per-variation never made sense — and shipping the wrong shape corrupted
  the row in `policy_rule_sets.policy` enough that `FetchGate` started
  returning a phantom 404 (see "Fixed" below).
- Variations now take two explicit lists matching the Console UI's "Gate
  Requirements" dialog: `required_controls` and `required_gates`. Each
  entry is a path (`"terraform.example.iac.scan"`) or an entity UUID; the
  provider resolves paths via `FetchControl` / `FetchGate` at apply time
  and ships the wire-shape `{<label>: <uuid>}` the gate-children CTE
  expects. Migration: replace each `policy = jsonencode({...})` with
  `required_controls = ["..."]` and/or `required_gates = ["..."]`.

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
- `fianu_index` resource for managing Fianu Index entities. An index is a
  reusable asset-scope definition (an `asset_type` + one or more CEL
  `expressions` with `combine_with` and `kind` strategy) that policies and
  gates reference instead of restating the CEL inline. Uses the dedicated
  REST shape (`POST/PATCH/GET/DELETE /api/entities/indexes`) rather than
  the generic `deploy_entity_file` multipart route — `CreateIndex`,
  `UpdateIndex`, `GetIndex`, `ArchiveIndex` on `external/pkg/sdk/v2`.
  Read hydration covers the envelope plus `asset_type` (the latter
  because it's `RequiresReplace`, so leaving it null post-import would
  force a destroy+create as soon as the user authored matching HCL).
  Other Detail fields stay user-authored to avoid drift against
  server-side canonicalisation. The wrapper's `IndexWithComputeState`
  (member count, recompute timestamps) is not surfaced since it changes
  independently of user intent.
- `fianu_gate` criteria + protected-scope (`pods[].matching`) now accept
  the same three input shapes as `fianu_policy.detail.variations[].criteria`:
  `asset` (per-criteria asset type binding), `expressions` (inline CEL —
  demoted from required to optional), and `indexes` (references to existing
  index entities by `id` or `path`). Mirrors the canonical
  `PolicyAssetGroup` write shape on gates so reusable index entities can
  scope gate variations and pod matching scopes alongside policies.
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

[Unreleased]: https://github.com/fianulabs/terraform-provider-fianu/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/fianulabs/terraform-provider-fianu/compare/v0.2.3...v0.3.0
[0.2.3]: https://github.com/fianulabs/terraform-provider-fianu/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/fianulabs/terraform-provider-fianu/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/fianulabs/terraform-provider-fianu/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/fianulabs/terraform-provider-fianu/compare/v0.1.31...v0.2.0
[0.1.0]: https://github.com/fianulabs/terraform-provider-fianu/releases/tag/v0.1.0
