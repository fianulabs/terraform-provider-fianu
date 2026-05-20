# `add` mode — file-by-file checklist

This is the canonical procedure for wiring a new Fianu entity into the provider. The order matters: every step has prerequisites in earlier steps.

`<entity>` is the lowercase singular name (`gate`, `policy`, `environment`, `target`, `collection`). `<Entity>` is the same name capitalised.

## 0. SDK-side prerequisites (verify before opening provider files)

The provider depends on the pinned `github.com/fianulabs/core/v2`. The SDK split matters — get this wrong and you'll waste hours:

- **Wire client (the only one the provider HTTP-calls):** `github.com/fianulabs/core/v2/external/pkg/sdk/v2`. This is the API-gateway-aware client. All four CRUD methods live here.
- **Legacy in-cluster client:** `github.com/fianulabs/core/v2/external/pkg/clients/fianu`. Despite being in `external/`, this speaks internal `/controls/...` paths and 404s when called through the gateway. Do **not** use it for HTTP. The only reason to import it is the in-memory builder helpers (e.g., `fianu.NewControlBuilder`) — and those are *optional*, not required (see "builder is optional" below).
- **Entity types:** `github.com/fianulabs/core/v2/external/db/types/fianu/entities` — `entities.Control`, `entities.Policy`, etc. This is the canonical struct shape. Do **not** use the versioned `policies/v2.x.x/` or similar legacy packages on the provider side — those are wire shapes the server may still accept for backward compat, not what the provider should marshal.

Required-or-escalate checks:

```bash
# REQUIRED: EntityType constant
grep "EntityType<Entity>" "$(go env GOMODCACHE)"/github.com/fianulabs/core/v2@*/external/db/variables/types.go

# REQUIRED: canonical entity struct (StandardEntity[<Entity>Detail])
ls "$(go env GOMODCACHE)"/github.com/fianulabs/core/v2@*/external/db/types/fianu/entities/<entity>.go

# REQUIRED: SDK Fetch<Entity> and Archive<Entity> methods, with the *right* return type
grep -n "func (c \*Client) Fetch<Entity>\|func (c \*Client) Archive<Entity>" \
  "$(go env GOMODCACHE)"/github.com/fianulabs/core/v2@*/external/pkg/sdk/v2/zz_generated_console.go
```

That last grep is load-bearing. **`Fetch<Entity>` MUST return `*entities.<Entity>`**, not a legacy versioned type. The auto-generated SDK pulls return types from swagger annotations server-side; if the annotation points at the wrong model, FetchXxx silently returns the wrong shape and Read hydration breaks. If you see `*policiesvXXX.Entity` or any non-`entities.<Entity>` return type, stop and escalate to fix the swagger annotation server-side. (This bit us on `fianu_policy` — burned an afternoon.)

If `EntityType<Entity>` or the entity struct is missing, stop and escalate to the `entity-management` user-level skill in `../core`.

**The builder is OPTIONAL.** `fianu.New<Entity>Builder` is a convenience for constructing the wire entity struct fluently. If it exists, use it (matches the CLI's path, makes diffs cleaner). If it doesn't exist (as was the case for `fianu_policy`), just construct the `*entities.<Entity>` struct directly — see `internal/resources/policy/resource.go::buildEntity` for the direct-construction pattern. Do **not** escalate just because the builder is missing.

## 1. Create the resource package

```
internal/resources/<entity>/
├── resource.go            # CRUD + Schema + ImportState + IdentitySchema
├── <section1>.go          # one file per Detail subsection (mirror control/, policy/)
├── <section2>.go
└── resource_test.go       # acceptance tests + per-package stub
```

**Template:** copy `internal/resources/policy/resource.go` and adapt — it's the newest entity and has the cleanest SDK-v2 wiring. `internal/resources/control/resource.go` is also fine but ships extra concerns (action triggers, evaluation cases) that may not apply.

Pieces to keep verbatim:

- `package <entity>` + the MPL header
- Compile-time interface assertions for `resource.Resource`, `ResourceWithConfigure`, `ResourceWithImportState`, `ResourceWithIdentity`
- `const entityType = "<entity>"`
- `NewResource() resource.Resource` factory — this is what the provider package registers
- `<entity>Model` struct embeds `base.EnvelopeModel`; `Detail` carries per-entity fields
- `Schema` calls `base.EnvelopeAttributes()` and merges in the Detail block
- `IdentitySchema` calls `base.EnvelopeIdentitySchema()`
- `Configure` asserts `*sdk.Client` (the SDK-v2 client) — **NOT** `*fianu.Client`
- `Create` / `Update` extract into a shared `deploy<Entity>(ctx, plan) (*transportv1.DeployEntityFileResponse, diag.Diagnostics)` helper that builds the entity, JSON-marshals it, and calls `client.DeployEntityFile(ctx, transportv1.DeployEntityFileRequest{General: …}, entityJSON, false)`
- `Read` calls `client.Fetch<Entity>(ctx, path, nil, nil)` and uses `errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound` to distinguish state-evict-on-404 from any-other-error-is-a-diagnostic
- `Delete` calls `client.Archive<Entity>(ctx, uuid)`, treats 404 as already-gone (happy path)
- `ImportState` uses `base.ParseID` + `resp.State.SetAttribute(ctx, path.Root("path"), key)`

### Hydration Rule (load-bearing)

Read intentionally hydrates only the envelope and the minimum set of user-authored identity fields. For `control`, that's the `ControlInfo` trio (`full_name`, `display_key`, `description`). For `policy`, hydration is envelope-only (no Detail fields). Do **not** hydrate rich Detail sections from the server — the server canonicalises and reorders, and any round-trip difference would surface as spurious drift on the next plan.

When the SDK's `Fetch<Entity>` returns the canonical `*entities.<Entity>`, hydration is a one-liner:

```go
env := base.EnvelopeFromStandardEntity(entityType, &fetched.StandardEntity)
return m.Hydrate(ctx, env)
```

### StandardEntity dual-embed pitfall (load-bearing)

**Some entity structs in `db/types/fianu/entities` embed BOTH `StandardEntity[<Entity>Detail]` AND the bare `<Entity>Detail`.** `entities.Policy` is the example:

```go
type Policy struct {
    StandardEntity[PolicyDetail] `yaml:",inline"`     // Detail field nested under JSON "detail"
    PolicyDetail                 `yaml:",inline" json:",inline"`  // inlined to top level
    General                      General `json:"general"`
}
```

Two ways to set the same conceptual field, e.g. `Control.Path`:

- `p.StandardEntity.Detail.Control.Path = "..."` — lives under JSON `"detail"` key, **survives marshal/unmarshal round-trip**
- `p.PolicyDetail.Control.Path = "..."` — gets inlined to top level, **lost on round-trip** because of JSON tag collisions with StandardEntity fields and the entity's custom (Un)MarshalJSON

**Always write to `p.StandardEntity.Detail.<field>`** for entities with this dual-embed shape (and assert on it in tests too — `captured.StandardEntity.Detail.<field>`, not the inline). Control doesn't have the dual embed, so `c.Detail.<field>` is unambiguous there — but check the entity definition before assuming.

**Quick smoke test for round-trip behavior** when adding any new entity:

```go
// in a scratch _test.go
p := &fianu_entities.<Entity>{}
// ...populate fields...
b, _ := json.Marshal(p)
var p2 fianu_entities.<Entity>
json.Unmarshal(b, &p2)
// Does p2 have the same Detail fields as p?
```

If round-trip drops fields, you're writing to the wrong embed. Switch to `p.StandardEntity.Detail.*`.

### Detail subsections

Each Detail subsection lives in its own file (mirroring `evaluation.go`/`relations.go`/`assets.go`/`measures.go` in `control/`, or `variations.go`/`override.go`/`criteria.go` in `policy/`). Each file owns:

1. The Terraform-side model type (e.g., `variationModel`)
2. The `schema.Attribute` / nested block definition exported for use in `resource.go::Schema`
3. The HCL-model → wire-entity translator (e.g., `buildVariations`, `(*overrideModel).toEntity`)
4. Any constants — magic strings (like `"apply"`/`"exempt"`) must come from the SDK's typed constants (`fianu_entities.PolicyEffectApply`, etc.), not be hardcoded

When a Detail field is too dynamic for HCL (e.g., `map[string]any` of arbitrary metric overrides), expose it as a `types.String` containing JSON the user authors via `jsonencode({...})`. Translator parses with `json.Unmarshal`. Don't try to express truly dynamic maps in the framework's schema — it'll fight you.

## 2. Register on the provider

`internal/provider/provider.go`:

- Import `"github.com/fianulabs/terraform-provider-fianu/internal/resources/<entity>"`
- Append `<entity>.NewResource` to the `Resources()` slice

## 3. Acceptance tests — per-package stub (don't cross packages)

The control package's `newConsoleStub` is a useful reference, but **don't try to extend it across packages**. Each entity package owns its own stub in its own `resource_test.go`. Cross-package extension creates awkward exported state and Go test-package boundaries that aren't worth it. `internal/resources/policy/resource_test.go::newPolicyStub` is the template — fork it, rename, adjust routes.

Routes the stub mounts (substitute `<entity>` and `<entity-type-route-segment>` — usually plural):

- `POST /api/entities/artifacts/deploy` — captures the entity via `decodeMultipart<Entity>` (parses the `payload` form field for the General envelope and the `file` part for the entity JSON), stores it on the stub via `atomic.Value`, returns a `DeployEntityFileResponse` with the SHA256 idempotency hash echoed back so re-deploys flip `action` from `"created"` → `"skipped"`.
- `GET /api/entities/<entities>/{key}` — echoes the captured entity back so Read doesn't drift.
- `DELETE /api/entities/archive/<entity>/{uuid}` — returns `{"status":"archived"}` and bumps the archive counter.
- `POST /api/entities/artifacts/test` — only if the entity supports server-side testing.

**Wire format reminder:** the provider sends multipart with two parts (`payload` JSON envelope + `file` entity JSON). The legacy `X-Fianu-Raw-Content` header is gone — don't reference it in new stubs.

Tests every entity needs:

- `TestAccFianu<Entity>_Minimal` — bare envelope + required Detail fields, asserts the captured entity has the right shape and `id == "<entity>/<path>"`
- `TestAccFianu<Entity>_FullSpec` — every Detail subsection populated
- `TestAccFianu<Entity>_Idempotent` — `plancheck.ExpectEmptyPlan()` on a second apply; proves Read hydration doesn't drift
- Round-trip assertions on any byte-for-byte payload field (cf. `TestAccFianuControl_EvaluationContent_RoundTrips`)

For `TestAccFianuControl_Import`-style tests, `terraform import` followed by no-op plan is the gold standard, but they're load-bearing on ImportState's hydration logic — skip in the initial PR if Detail hydration is non-trivial; add in a follow-up.

## 4. Action (only if the entity supports server-side testing)

If the entity has a `/api/entities/artifacts/test` use case (controls do; gates may; policies don't), mirror `internal/actions/control_test/`:

```
internal/actions/<entity>_test/
├── action.go
└── action_test.go
```

Register in `provider.go::Actions()`. The action's input schema should mirror the corresponding section in the resource so users can share cases via `locals`. Action client type is also `*sdk.Client` — not `*fianu.Client`.

## 5. Examples

```
examples/resources/fianu_<entity>/
├── resource.tf              # canonical — what tfplugindocs renders
├── import.sh
├── simple/
│   └── resource.tf          # minimum-viable example
└── advanced/                # OR a domain-named variant (see below)
    └── resource.tf
```

**Subdirectory naming — two valid patterns:**

- **`simple/` + `advanced/`** when the Detail surface has graded complexity (cf. `examples/resources/fianu_policy/`). Use this when "more knobs" is the main axis of variation — e.g., policy variations with vs without CEL criteria.
- **Domain-named variants** (`sast_checkmarx/`, `unit_tests_pytest/`, etc.) when distinct real-world use cases are the main axis (cf. `examples/resources/fianu_control/`). Use this when each variant maps to a different vendor/integration.

Top-level `resource.tf` is what `tfplugindocs` reads for the Registry page — keep it minimum-viable. Variants are reference-quality copies users browse to via the GitHub examples directory.

## 6. Docs

```bash
go generate ./...
```

Commit the regenerated `docs/resources/<entity>.md` (and any index changes) in the same PR. The CI `docs` job fails the build if `docs/` drifts.

## 7. README + CHANGELOG

- `README.md` — flip the roadmap entry for `<entity>` from ⏳ to ✅.
- `CHANGELOG.md` — add an entry under `## [Unreleased]`:
  ```markdown
  ### Added
  - `fianu_<entity>` resource for managing Fianu <Entity> entities.
  ```

## 8. Verify before opening the PR

```bash
go vet ./...
TF_ACC=1 go test ./...
go generate ./...
git diff --quiet -- docs/        # must be clean
```

The PR template checklist mirrors this list — tick each box.
