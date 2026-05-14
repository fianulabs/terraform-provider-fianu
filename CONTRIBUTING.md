# Contributing to terraform-provider-fianu

Thanks for your interest in contributing! This guide covers the local
development workflow, testing, and the conventions we follow for commits and
pull requests.

## Prerequisites

- Go ≥ 1.26 (the version pinned in `go.mod`)
- Terraform CLI ≥ 1.14 (required for the action-test harness; framework v1.16+
  actions need this CLI version)
- `GOPRIVATE=github.com/fianulabs` exported in your shell so `go get` can pull
  the SDK module
- Access to a Fianu Console (or rely on the in-tree `httptest` stub — see
  "Tests" below)

## Local development loop

```bash
# Build and install the provider binary into $GOPATH/bin
go install .

# Wire ~/.terraformrc so Terraform picks up your local build
cat <<EOF > ~/.terraformrc
provider_installation {
  dev_overrides {
    "fianulabs/fianu" = "$(go env GOPATH)/bin"
  }
  direct {}
}
EOF
```

With `dev_overrides` set, `terraform init` is skipped for the `fianulabs/fianu`
provider — your changes to the binary are picked up on the next `terraform
plan`/`apply` without re-publishing.

## Tests

```bash
# Unit + acceptance tests (the acceptance harness uses httptest, not a live
# Fianu Console, so this works offline)
TF_ACC=1 go test ./...

# Single test
TF_ACC=1 go test ./internal/resources/control -run TestAccFianuControl_FullSpec -v

# Static checks
go vet ./...
```

Action tests require the `terraform` CLI on `$PATH` (≥ 1.14). The provider
itself does not.

## Regenerating documentation

The `docs/` directory is generated from the live resource schemas and the
`examples/` fixtures. Regenerate after any schema change:

```bash
go generate ./...
```

Commit the regenerated `docs/` alongside your code change in the same PR.

## Adding a new entity type

The CLAUDE.md at the repo root + the `adding-entity` skill under
`.claude/skills/adding-entity/` document the full checklist: where to place
files, what to register in `internal/provider/provider.go`, how to extend the
acceptance-test stub, and which `examples/` and `docs/` artifacts to refresh.
Follow that checklist rather than ad-hoc patterns from other Terraform
providers — the `internal/resources/base/` envelope is load-bearing.

## Commits and pull requests

- Keep commits focused; favour one logical change per commit.
- Subject lines are imperative ("Add gate resource", not "Added" or "Adds").
- Include a CHANGELOG entry under `## [Unreleased]` for user-visible changes.
- PRs should pass `go vet ./...` and `TF_ACC=1 go test ./...` before review.
- The PR template (`.github/pull_request_template.md`) lists the full checklist.

## Reporting bugs / requesting features

Use the [issue templates](.github/ISSUE_TEMPLATE/). For security issues, see
[`SECURITY.md`](./SECURITY.md) — please don't open a public issue for
vulnerabilities.

## License

By contributing, you agree that your contributions will be licensed under the
[Mozilla Public License 2.0](./LICENSE).
