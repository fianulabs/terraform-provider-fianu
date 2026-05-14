---
name: adding-entity
description: Use when adding a new Fianu entity type to the Terraform provider (gate, policy, environment, target, collection) — covers the full file checklist (resource, schema, hydration, tests, examples, docs, README roadmap) and includes an audit mode that diffs the pinned fianulabs/core/v2 SDK against newer commits in ../core to detect new entity types, breaking field changes, new wire constants, or new routes that may require provider work.
---

# Adding an entity to terraform-provider-fianu

This skill has two modes. Pick the one that matches the user's intent.

## Mode 1 — `add`: wire up a new entity type as a Terraform resource

**Triggers:** "add the gate resource", "wire up `fianu_policy`", "implement `fianu_environment`", "scaffold a new entity in the provider".

The provider's only shipped entity today is `fianu_control` (resource) + `fianu_control_test` (action). Five more entity types are stubbed in the README roadmap: **gate, policy, environment, target, collection**. They all share the same envelope (`internal/resources/base/`), so adding one is mechanical *if* you follow the checklist — and load-bearing details are easy to miss if you don't.

The full file-by-file procedure, including the canonical templates to copy from and the SDK-side prerequisites to verify in `../core`, lives in **[`add-checklist.md`](./add-checklist.md)**. Read it before opening any files.

### Quick sanity checks before starting

1. **Is the SDK builder ready?** Look for `github.com/fianulabs/core/v2/external/pkg/clients/fianu/builder_<entity>.go`. If it doesn't exist, stop and escalate to the `entity-management` user-level skill in `../core` — the provider cannot land without it.
2. **Is the entity type constant present?** Grep `external/db/variables/types.go` for `EntityType<Name>`. Same deal — escalate if missing.
3. **Confirm the wire routes exist.** All entities go through `POST /entities/artifacts/deploy` (deploy) and `GET /<type>s/<key>` (read), so this is usually free, but eyeball `external/transport/http/v1/` if anything looks unusual.

If all three pass, proceed with the checklist.

### Critical reuse points (never reimplement these)

| Concern | Lives in | What it gives you |
|---|---|---|
| Envelope schema (id, uuid, path, name, metadata, version, parents, children) | `internal/resources/base/envelope.go` | `EnvelopeAttributes()` for `Schema`, `EnvelopeModel` to embed in your resource model |
| Hydrating envelope from server payload | `internal/resources/base/envelope_builders.go` | `EnvelopeFromStandardEntity[T]`, `EnvelopeFromDeployMetadata` |
| Composite resource ID (`<type>/<key>`) | `internal/resources/base/identifier.go` | `FormatID`, `ParseID` |
| Structured Resource Identity (Terraform 1.12+ import) | `internal/resources/base/identity.go` | `EnvelopeIdentitySchema()` |
| SDK builder for the wire entity | `github.com/fianulabs/core/v2/external/pkg/clients/fianu/builder_<entity>.go` | `fianu.New<X>Builder` — single source of truth, shared with `pkg/entities_files/<entity>_deployer.go` server-side |

### Load-bearing gotchas

These have bitten contributors before. The checklist enforces them, but you need to understand *why*:

- **Plan-modifier rules in the envelope are deliberate.** `path` is `RequiresReplace`. Computed envelope fields use `UseStateForUnknown` *except* `version.status` and `version.state`, which intentionally surface server-side workflow drift on each plan. Don't "fix" this.
- **Read hydration is intentionally shallow.** `hydrateFrom<Entity>` only reads back the envelope and minimal user-authored ID fields (for control, that's the `ControlInfo` trio: `full_name`/`display_key`/`description`). Richer Detail sections stay user-authored — hydrating them would create false drift from server-side canonicalisation/ordering.
- **Idempotency is server-driven.** The server hashes the entity and returns `action: "skipped"` when content matches. Don't try to short-circuit this in the provider — let `Create`/`Update` always call deploy.
- **Acceptance tests extend the existing stub.** `internal/resources/control/resource_test.go::newConsoleStub` already impersonates Console (decodes `X-Fianu-Raw-Content`, mimics the idempotency gate, echoes the entity back on GET). Add routes to it; **don't spin up a second `httptest.Server`.**

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
