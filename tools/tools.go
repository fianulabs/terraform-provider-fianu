// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

//go:build tools
// +build tools

// Package tools pins development-time dependencies (tfplugindocs) so they
// stay versioned alongside the provider but don't ship in the release binary.
//
// Generate documentation:
//
//	go generate ./...
//
// The tfplugindocs binary reads the resource schemas + examples/ and emits
// docs/ in the format expected by the Hashicorp Terraform Registry.
package tools

import (
	_ "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs"
)
