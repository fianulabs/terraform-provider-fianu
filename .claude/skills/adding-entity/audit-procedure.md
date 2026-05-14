# `audit` mode — SDK drift procedure

Goal: produce a classified punch-list of changes in `github.com/fianulabs/core/v2` since the pinned version, with each finding mapped to a concrete provider-side follow-up.

This procedure is intentionally deterministic. Follow it step-by-step — the value is the consistency, not the cleverness.

## Prerequisites

- A local sibling checkout at `../core` (relative to the provider repo root).
- The pinned SDK version from `go.mod`.

```bash
# At the provider repo root
PIN=$(grep '^	github.com/fianulabs/core/v2 ' go.mod | awk '{print $2}')
echo "Pinned: $PIN"
```

If `../core` does not exist, stop and ask the user to clone it before continuing.

## Step 1 — List newer tags

```bash
git -C ../core fetch --tags --quiet
git -C ../core tag --sort=-version:refname \
  | awk '/^v2/' \
  | sed "/$PIN/q" \
  | head -n -1
```

Empty output = no newer tags = SDK is current. Exit and report ✅ "SDK is at the latest v2 tag."

Otherwise: the first line is the latest available tag. Call it `$LATEST`.

## Step 2 — Focused diff between pin and latest

The full diff is too noisy. Restrict to paths that produce provider-relevant signal:

```bash
git -C ../core diff --stat "$PIN".."$LATEST" -- \
  external/db/types/fianu/entities/ \
  external/db/variables/types.go \
  external/pkg/clients/fianu/ \
  external/pkg/variables/api.go \
  external/pkg/variables/console.go \
  external/transport/http/v1/
```

For each non-zero stat line, run a targeted diff to read the actual changes:

```bash
git -C ../core diff "$PIN".."$LATEST" -- <path>
```

## Step 3 — Classify each finding

Walk the diff output and bucket each change into one of these categories. Cite the `../core` path (and line numbers where useful) for every finding.

### 🆕 New entity ready to add

All three conditions must hold:

1. `external/db/types/fianu/entities/<entity>.go` — a new file (not just edits to existing ones) defining a `<Entity>Detail` struct and a wrapper around `StandardEntity[<Entity>Detail]`.
2. `external/db/variables/types.go` — a new `EntityType<Entity>` constant.
3. `external/pkg/clients/fianu/builder_<entity>.go` — a new `New<Entity>Builder` function.

Cross-reference against `README.md` roadmap: if it's listed as ⏳, this is an unblock signal. Recommend invoking `add` mode.

### ⚠️ Breaking change to a struct the provider already consumes

Grep the provider for imports/uses of the affected struct first:

```bash
grep -rn "fianu_entities\.<Type>\|entities\.<Type>" internal/
```

If the provider references the type and the diff shows:

- a renamed field
- a removed field
- a retyped field (e.g., `string` → `*string`, `[]X` → `map[string]X`)
- a renamed or removed exported function/method

…flag it. Note the specific file:line in `../core` and the affected provider file(s).

### 🆕 New wire constant / header

Look in:

- `external/pkg/variables/api.go` — `XFianu*` headers, `CR*` ControlResource constants, MIME types.
- `external/pkg/variables/console.go` — Console-specific constants.

A new header or constant may signal a transport/auth change the provider should opt into. Cite the constant name and the introducing commit.

### 🆕 New route

Look in `external/transport/http/v1/` for new handler registrations. Pay particular attention to:

- New `/entities/artifacts/*` endpoints — may unlock new actions.
- New `GET /<type>s/<key>` shapes — may unlock new data sources.
- Changes to `DELETE /archive/<type>/<uuid>` semantics — may affect provider `Delete`.

### ✅ Safe SDK bump

If the cumulative diff shows only:

- Patch-level dependency updates
- Test-only changes
- Comment/docs changes
- Internal refactors that don't change exported signatures

…classify as safe and recommend bumping the pin without further work.

## Step 4 — Output format

Produce a single Markdown report with this shape:

```markdown
# SDK audit: <PIN> → <LATEST>

**Pinned:** <PIN>
**Latest available:** <LATEST>
**Tags traversed:** <count>

## Findings

### 🆕 New entities ready to add
- **<entity>** — builder at `../core/external/pkg/clients/fianu/builder_<entity>.go:1`, type at `../core/external/db/types/fianu/entities/<entity>.go:1`, constant at `../core/external/db/variables/types.go:NN`. Roadmap status in `README.md`: ⏳ → can be flipped after `add` mode runs.

### ⚠️ Breaking changes
- **<type>.<field>** — renamed `Foo` → `Bar` at `../core/external/db/types/fianu/entities/<type>.go:NN`. Provider references: `internal/resources/<x>/<file>.go:NN`. Action: update field reference and re-run tests.

### 🆕 New wire constants
- **<NAME>** — added at `../core/external/pkg/variables/api.go:NN`. Used by `<endpoint>`. Action: consider whether provider needs to send/honour this.

### 🆕 New routes
- **`<METHOD> <path>`** — added at `../core/external/transport/http/v1/<file>.go:NN`. Action: evaluate for data-source or action.

### ✅ Safe (no provider work required)
- <summary line per safe change>

## Recommendation
<one-line: "Bump SDK pin to <LATEST> and run add mode for <entity>" / "Hold; <breaking change> needs investigation first" / "Safe to bump as-is" />
```

## Step 5 — What this mode does NOT do

- Do **not** modify `go.mod` or any provider file.
- Do **not** open issues or PRs automatically.
- Do **not** invoke `add` mode without explicit user confirmation, even if a 🆕 new entity is found.

End by asking the user which findings to act on.
