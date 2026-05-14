// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package base

import (
	"fmt"
	"strings"
)

// FormatID composes the canonical Terraform resource ID for any Fianu
// entity-style resource: `<entity_type>/<entity_key>`. Self-describing in
// `terraform state list` and unambiguous on import.
func FormatID(entityType, entityKey string) string {
	return entityType + "/" + entityKey
}

// ParseID is the inverse of FormatID. It accepts either the composite
// `<entity_type>/<entity_key>` form (preferred) or a bare entity_key for
// backward-compatible import scripts. The expectedType argument lets the
// caller assert that the type prefix matches the resource being imported —
// importing `policy/foo` into a `fianu_control` resource is a user error
// and we want a clear message.
func ParseID(raw, expectedType string) (entityKey string, err error) {
	parts := strings.SplitN(raw, "/", 2)
	switch len(parts) {
	case 1:
		// Bare key — accept it but assume the caller is importing into the
		// right resource type.
		return parts[0], nil
	case 2:
		if parts[0] != expectedType {
			return "", fmt.Errorf("import id %q has type %q but expected %q", raw, parts[0], expectedType)
		}
		return parts[1], nil
	default:
		return "", fmt.Errorf("import id %q is not in the form <entity_type>/<entity_key>", raw)
	}
}
