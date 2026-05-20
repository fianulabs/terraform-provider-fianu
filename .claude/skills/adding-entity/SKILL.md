---
name: adding-entity
description: Use when adding a new Fianu entity type to the Terraform provider (gate, policy, environment, target, collection) — covers the full file checklist (resource, schema, hydration, tests, examples, docs, README roadmap) and includes an audit mode that diffs the pinned fianulabs/core/v2 SDK against newer commits in ../core to detect new entity types, breaking field changes, new wire constants, or new routes that may require provider work.
---

# Adding an entity to terraform-provider-fianu

This skill has two modes. Pick the one that matches the user's intent.

## Mode 1 — `add`: wire up a new entity type as a Terraform resource

**Triggers:** "add the gate resource", "wire up `fianu_policy`", "implement `fianu_environment`", "scaffold a new entity in the provider".

Shipped today: `fianu_control` (resource + `fianu_control_test` action) and `fianu_policy` (resource). Four more entity types are stubbed in the README roadmap: **gate, environment, target, collection**. They all share the same envelope (`internal/resources/base/`), so adding one is mechanical *if* you follow the checklist — and load-bearing details are easy to miss if you don't.

The full file-by-file procedure, including the canonical templates to copy from and the SDK-side prerequisites to verify in `../core`, lives in **[`add-checklist.md`](./add-checklist.md)**. Read it before opening any files.

### Quick sanity checks before starting

1. **Is `EntityType<Name>` defined?** Grep `external/db/variables/types.go`. **Required** — escalate to the `entity-management` user-level skill in `../core` if missing.
2. **Does the canonical entity struct exist?** Look for `external/db/types/fianu/entities/<entity>.go` (e.g., `entities.Policy`). **Required** — escalate if missing. This is *not* the same as a versioned package like `policies/v2.2.0/` — use the `entities` one.
3. **Does the SDK have `Fetch<Entity>` and `Archive<Entity>` with the right return type?** Grep `external/pkg/sdk/v2/zz_generated_console.go`. **`Fetch<Entity>` MUST return `*entities.<Entity>`** — not a legacy versioned struct. If the auto-generated SDK returns the wrong type, escalate to fix the swagger annotation server-side before touching the provider. (This bit us on `fianu_policy`.)
4. **Is the in-memory builder available?** Look for `external/pkg/clients/fianu/builder_<entity>.go`. **Optional** — use it if present (matches the CLI's construction path), otherwise construct the `*entities.<Entity>` struct directly. Do not escalate just because the builder is missing — `fianu_policy` ships without one.
5. **Confirm the wire routes exist.** All entities go through `POST /api/entities/artifacts/deploy` (deploy), `GET /api/entities/<entities>/:key` (read), and `DELETE /api/entities/archive/<entity>/:uuid` (delete). Usually free — eyeball `cmd/proxy/kodata/config/proxies/console/proxies.yaml` if anything looks unusual.

If 1–3 pass, proceed with the checklist.

### Critical reuse points (never reimplement these)

| Concern | Lives in | What it gives you |
|---|---|---|
| Envelope schema (id, uuid, path, name, metadata, version, parents, children) | `internal/resources/base/envelope.go` | `EnvelopeAttributes()` for `Schema`, `EnvelopeModel` to embed in your resource model |
| Hydrating envelope from server payload | `internal/resources/base/envelope_builders.go` | `EnvelopeFromStandardEntity[T]`, `EnvelopeFromDeployMetadata` |
| Composite resource ID (`<type>/<key>`) | `internal/resources/base/identifier.go` | `FormatID`, `ParseID` |
| Structured Resource Identity (Terraform 1.12+ import) | `internal/resources/base/identity.go` | `EnvelopeIdentitySchema()` |
| SDK HTTP client (the one that calls the gateway) | `github.com/fianulabs/core/v2/external/pkg/sdk/v2` | `sdk.Client.DeployEntityFile`, `Fetch<Entity>`, `Archive<Entity>` — this is the ONLY client the provider HTTP-calls |
| Canonical entity structs | `github.com/fianulabs/core/v2/external/db/types/fianu/entities` | `entities.Control`, `entities.Policy`, … — the wire shape you marshal |
| SDK builder for the wire entity (optional) | `github.com/fianulabs/core/v2/external/pkg/clients/fianu/builder_<entity>.go` | `fianu.New<X>Builder` — fluent constructor when it exists. If it doesn't, construct `*entities.<Entity>` directly (see `internal/resources/policy/resource.go::buildEntity`) |

### Load-bearing gotchas

These have bitten contributors before. The checklist enforces them, but you need to understand *why*:

- **Plan-modifier rules in the envelope are deliberate.** `path` is `RequiresReplace`. Computed envelope fields use `UseStateForUnknown` *except* `version.status` and `version.state`, which intentionally surface server-side workflow drift on each plan. Don't "fix" this.
- **Read hydration is intentionally shallow.** `hydrateFrom<Entity>` only reads back the envelope and minimal user-authored ID fields (for control, that's the `ControlInfo` trio: `full_name`/`display_key`/`description`; for policy, envelope-only). Richer Detail sections stay user-authored — hydrating them would create false drift from server-side canonicalisation/ordering.
- **Idempotency is server-driven.** The server hashes the entity and returns `action: "skipped"` when content matches. Don't try to short-circuit this in the provider — let `Create`/`Update` always call deploy.
- **StandardEntity dual-embed**, when present, makes one of the two paths to a Detail field silently drop on round-trip. `entities.Policy` is the canonical example: write to `p.StandardEntity.Detail.*`, not the directly-embedded `p.PolicyDetail.*`. Check the entity definition before assuming. (See `add-checklist.md` for the marshal/unmarshal smoke test.)
- **404 from Read evicts state; anything else surfaces a diagnostic.** Use `errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound` — not a blanket `if err != nil { RemoveResource }`. Silent eviction on transient errors is the bug that motivated the SDK-v2 migration.
- **Acceptance stubs are per-package.** `internal/resources/policy/resource_test.go::newPolicyStub` is the cleanest template. Don't try to share a stub across packages — Go test-package boundaries make cross-package extension awkward, and entity-specific multipart parsing differs enough that duplication is cheaper than abstraction.

### Don't forget the non-code surface

- `internal/provider/provider.go` — register in `Resources()` and (if applicable) `Actions()`.
- `examples/resources/fianu_<entity>/` — `resource.tf` + `import.sh` minimum. `tfplugindocs` reads these.
- `README.md` — flip the roadmap status from ⏳ to ✅ in the entity table.
- `CHANGELOG.md` — add an entry under `## [Unreleased]`.
- `go generate ./...` — regenerate `docs/`. CI verifies the result is clean.

---

## Mode 2 — `audit`: diff the pinned SDK against newer core commits

**Triggers:** "audit recent core changes", "what's new in the SDK since we last pinned", "check if any breaking changes landed in core", "what entities can we add now".

The provider depends on a pinned `github.com/fianulabs/core/v2` version. New SDK work (new entity types, new builders, new wire constants, breaking field changes) doesn't surface here automatically — this mode produces a classified punch-list of changes worth acting on.

The exact `git`/`grep` procedure, the paths to diff, and the classification rules live in **[`audit-procedure.md`](./audit-procedure.md)**. Follow it step-by-step — the value of this mode is the consistency, not the cleverness.

### Output shape

A punch-list grouped by classification:

- 🆕 **New entity ready to add** — builder + entity struct + EntityType constant all present in core. Cross-reference with `README.md` roadmap.
- ⚠️ **Breaking change** — field renamed/removed/retyped on a struct the provider already consumes (cross-reference imports in `internal/resources/control/`).
- 🆕 **New wire constant / header** — may need auth or transport update in `internal/provider/provider.go`.
- 🆕 **New route** — may unlock a data-source or action.
- ✅ **Safe SDK bump** — patch-level changes only, no signature drift.

Each finding cites `../core` paths (file:line where possible) and proposes a concrete follow-up (open issue, bump SDK, file CHANGELOG entry, etc.).

### What this mode does NOT do

- It does **not** bump the SDK version in `go.mod`. That's a separate, user-driven decision.
- It does **not** open issues or PRs automatically — it produces a report and asks the user what to action.
- It does **not** modify any provider files.

---

## When to invoke which mode

| User says... | Mode |
|---|---|
| "Add the gate resource" / "implement `fianu_<x>`" | `add` |
| "What's new in core since we last pinned" / "audit the SDK" | `audit` |
| "Is the policy entity ready to add yet?" | `audit` first, then `add` if the report says yes |
| "Bump the SDK" | `audit` first to surface any breaking changes; the bump itself is out of scope |
