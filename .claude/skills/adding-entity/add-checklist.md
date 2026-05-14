# `add` mode — file-by-file checklist

This is the canonical procedure for wiring a new Fianu entity into the provider. The order matters: every step has prerequisites in earlier steps.

`<entity>` is the lowercase singular name (`gate`, `policy`, `environment`, `target`, `collection`). `<Entity>` is the same name capitalised.

## 0. SDK-side prerequisites (verify before opening provider files)

The provider depends on the pinned `github.com/fianulabs/core/v2`. None of the following can be added on the provider side:

```bash
# Required: SDK builder for the wire entity
ls "$(go env GOMODCACHE)"/github.com/fianulabs/core/v2@*/external/pkg/clients/fianu/builder_<entity>.go

# Required: EntityType constant
grep "EntityType<Entity>" "$(go env GOMODCACHE)"/github.com/fianulabs/core/v2@*/external/db/variables/types.go

# Required: entity struct
ls "$(go env GOMODCACHE)"/github.com/fianulabs/core/v2@*/external/db/types/fianu/entities/<entity>.go
```

If any of these are missing, stop and escalate to the `entity-management` user-level skill in `../core`. Do not stub on the provider side.

## 1. Create the resource package

```
internal/resources/<entity>/
├── resource.go            # CRUD + Schema + ImportState + IdentitySchema
├── <section1>.go          # one file per Detail subsection (mirror control/)
├── <section2>.go
└── resource_test.go       # acceptance tests
```

**Template:** copy `internal/resources/control/resource.go` and adapt. The structural pieces to keep verbatim:

- `package <entity>` + the MPL header
- Compile-time interface assertions for `resource.Resource`, `ResourceWithConfigure`, `ResourceWithImportState`, `ResourceWithIdentity`
- `const entityType = "<entity>"`
- `NewResource() resource.Resource` factory — this is what the provider package registers
- `<entity>Model` struct embeds `base.EnvelopeModel`; `Detail` carries per-entity fields
- `Schema` calls `base.EnvelopeAttributes()` and merges in the Detail block
- `IdentitySchema` calls `base.EnvelopeIdentitySchema()`
- `Create`/`Update` always call `client.DeployEntity` — let server-side hashing handle idempotency
- `Read` uses `hydrateFrom<Entity>` which delegates to `base.EnvelopeFromStandardEntity[T]`/`EnvelopeFromDeployMetadata` and reads back **only** the envelope + minimal user-authored ID fields (see Hydration Rule below)
- `Delete` calls `client.ArchiveEntity` with the UUID
- `ImportState` uses `base.ParseID`

### Hydration Rule (load-bearing)

Read intentionally hydrates only the envelope and the minimum set of user-authored identity fields. For `control`, that's the `ControlInfo` trio (`full_name`, `display_key`, `description`). Do **not** hydrate rich Detail sections from the server — the server canonicalises and reorders, and any round-trip difference would surface as spurious drift on the next plan.

### Detail subsections

Each Detail subsection lives in its own file (mirroring `evaluation.go`/`relations.go`/`assets.go`/`measures.go` in `control/`). Each file owns:

1. The Terraform-side model type (e.g., `evaluationCaseModel`)
2. The `schema.Block` definition exported for use in `resource.go::Schema`
3. The HCL-model → wire-entity translator (e.g., `toEvaluation`, `toResults`)
4. Any constants — magic strings (like `"pass"`, `"fail"` result keys) must come from the SDK's typed constants, not be hardcoded

## 2. Register on the provider

`internal/provider/provider.go`:

- Import `"github.com/fianulabs/terraform-provider-fianu/internal/resources/<entity>"`
- Append `<entity>.NewResource` to the `Resources()` slice

## 3. Acceptance tests — extend the stub, don't replace it

`internal/resources/control/resource_test.go::newConsoleStub` already impersonates Console:

- Decodes `X-Fianu-Raw-Content` (base64 JSON) on every deploy/test call
- Stores the decoded entity on the stub via `atomic.Value`
- First deploy returns `action="created"`; repeats with the same `X-Fianu-CI-System-Hash` return `action="skipped"`
- `GET /<type>s/<key>` echoes the deployed entity back so Read doesn't drift

**Pattern:** add routes to the existing stub for the new entity type. Don't spin up a second `httptest.Server` — tests should share fixtures and assertions where possible.

For each new resource, the test file needs:

- `TestAccFianu<Entity>_Minimal` — bare envelope + required Detail fields
- `TestAccFianu<Entity>_FullSpec` — every Detail subsection populated
- `TestAccFianu<Entity>_Idempotent` — two consecutive applies; second returns `skipped`
- `TestAccFianu<Entity>_Import` — `terraform import` followed by no-op plan
- Round-trip assertions on any byte-for-byte payload field (cf. `TestAccFianuControl_EvaluationContent_RoundTrips`)

## 4. Action (only if the entity supports server-side testing)

If the entity has a `/entities/artifacts/test` use case (controls do; gates/policies may), mirror `internal/actions/control_test/`:

```
internal/actions/<entity>_test/
├── action.go
└── action_test.go
```

Register in `provider.go::Actions()`. The action's input schema should mirror the corresponding section in the resource so users can share cases via `locals`.

## 5. Examples

```
examples/resources/fianu_<entity>/
├── resource.tf
└── import.sh
```

`tfplugindocs` reads these to populate the Registry-rendered docs. Variants (real-world configurations) live in subdirectories — see `examples/resources/fianu_control/{sast_checkmarx,unit_tests_pytest,container_scan_wiz}/` for the pattern.

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
