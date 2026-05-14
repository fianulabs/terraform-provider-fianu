// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package main

// Run `go generate ./...` to regenerate Registry-format markdown under docs/
// from the live resource schemas plus the examples/ fixtures. The tfplugindocs
// dependency itself is pinned via tools/tools.go (build-tagged so it doesn't
// ship in the release binary).
//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate --provider-name fianu
