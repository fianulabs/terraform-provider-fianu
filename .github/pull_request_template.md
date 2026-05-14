<!--
Thanks for the contribution! A couple of housekeeping notes before you submit:

  * For security fixes, please follow SECURITY.md instead of opening a public PR.
  * The release pipeline only runs on tag pushes; PR-side CI (lint + tests) gates this PR.
-->

## Summary

<!-- One or two sentences on what this PR changes and why. -->

## Scope of change

<!-- Tick all that apply. -->

- [ ] New resource / data source / action
- [ ] Schema change on an existing resource (additive)
- [ ] Schema change on an existing resource (breaking)
- [ ] Bug fix
- [ ] SDK bump (`github.com/fianulabs/core/v2`)
- [ ] Docs / examples only
- [ ] CI / release pipeline
- [ ] Internal refactor (no user-visible change)

## Pre-merge checklist

- [ ] `go vet ./...` passes
- [ ] `TF_ACC=1 go test ./...` passes locally
- [ ] `go generate ./...` re-run; `docs/` diff committed in this PR
- [ ] `examples/` updated if the schema changed
- [ ] `CHANGELOG.md` updated under `## [Unreleased]` for user-visible changes
- [ ] README roadmap flipped (only for new entity types)

## Reproducer / verification

<!--
Paste the HCL you used to exercise the change, and the `terraform plan`/`apply` output
that proves it works end-to-end. For bug fixes, include a regression test reference.
-->

```hcl

```

## Related issues

<!-- e.g. "Closes #123", "Refs #456", or "N/A" -->
