// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package policy

import (
	"context"

	fianu_entities "github.com/fianulabs/core/v2/external/db/types/fianu/entities"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	sdk "github.com/fianulabs/core/v2/external/pkg/sdk/v2"
)

// policyKind parameterises the one resource implementation over the two entity
// types that share the policy entity.
//
// An exception IS a policy on the server: the same *entities.Policy struct, the
// same PolicyDeployer and the same tester, both registered under `policy` and
// `policy_exception` in core's pkg/entities_files/handler.go. What differs is
// the entity type, and the server derives its create-path routing from
// `detail.type` (pkg/entities_files/policy_deployer.go — `isException`), then
// stores and reads each type from its own partition.
//
// That last part is why these are two Terraform resources rather than one with
// a `type` attribute. Reads filter on entity type (`executeFetch` vs
// `executeFetchException` in core's pkg/policies/db.go), so an exception is
// invisible to the policy read route and vice versa. Entity type is also baked
// into the composite resource ID and the identity schema, so it could never be
// an in-place-updatable attribute anyway.
type policyKind struct {
	// entityType is the composite resource-ID prefix (`<entity_type>/<key>`),
	// the identity schema's entity_type, and the value sent as
	// General.entityType on deploy.
	entityType string

	// typeNameSuffix is appended to the provider prefix — `fianu_<suffix>`.
	typeNameSuffix string

	// dbType is the server-side EntityType stamped on the deploy envelope.
	dbType db_vars.EntityType

	// allowedPolicyTypes are the values `detail.type` accepts. Deliberately
	// narrower than entities.PolicyType's full set on each kind: `exception`
	// belongs to fianu_policy_exception and nothing else, because a
	// fianu_policy carrying it would deploy as an exception and then 404 on
	// every refresh.
	allowedPolicyTypes []string

	// defaultPolicyType, when non-empty, makes `detail.type` optional and
	// supplies the value. Only the exception kind sets it — there is exactly
	// one legal value, so requiring the user to type it is noise.
	defaultPolicyType string

	// resourceDescription is the schema's MarkdownDescription.
	resourceDescription string

	// typeDescription documents `detail.type` for this kind.
	typeDescription string

	// fetch and archive select the entity-type-specific SDK routes.
	fetch   func(context.Context, *sdk.Client, string) (*fianu_entities.Policy, error)
	archive func(context.Context, *sdk.Client, string) error
}

// importedPolicyType is the placeholder `detail.type` ImportState writes so the
// post-import Read can decode state. For an exception there is exactly one
// legal value, so it is the real one; for a policy the user's HCL supplies it
// on the next plan and only the shape matters here.
func (k policyKind) importedPolicyType() string {
	if k.defaultPolicyType != "" {
		return k.defaultPolicyType
	}
	return string(fianu_entities.PolicyTypeStandard)
}

var standardKind = policyKind{
	entityType:     "policy",
	typeNameSuffix: "policy",
	dbType:         db_vars.EntityTypePolicy,
	allowedPolicyTypes: []string{
		string(fianu_entities.PolicyTypeStandard),
		string(fianu_entities.PolicyTypeTarget),
	},
	resourceDescription: "Manages a Fianu compliance policy. Policies bind a control's evaluation logic to the asset scope it gets applied to, and let you parameterise the control via per-variation metric overrides.",
	typeDescription:     "Policy type. One of `standard`, `target`. For `exception`, use the `fianu_policy_exception` resource — exceptions are stored and read as a separate entity type.",
	fetch: func(ctx context.Context, c *sdk.Client, key string) (*fianu_entities.Policy, error) {
		return c.FetchPolicy(ctx, key, nil, nil)
	},
	archive: func(ctx context.Context, c *sdk.Client, uuid string) error {
		_, err := c.ArchivePolicy(ctx, uuid)
		return err
	},
}

var exceptionKind = policyKind{
	entityType:          "policy_exception",
	typeNameSuffix:      "policy_exception",
	dbType:              db_vars.EntityTypePolicyException,
	allowedPolicyTypes:  []string{string(fianu_entities.PolicyTypeException)},
	defaultPolicyType:   string(fianu_entities.PolicyTypeException),
	resourceDescription: "Manages a Fianu policy exception. An exception carries the same shape as a `fianu_policy` — the same control binding and the same variations — but is stored as its own entity type and takes precedence over standard policies when both match an asset.",
	typeDescription:     "Policy type. Always `exception` for this resource; omit it.",
	fetch: func(ctx context.Context, c *sdk.Client, key string) (*fianu_entities.Policy, error) {
		return c.FetchPolicyException(ctx, key, nil)
	},
	archive: func(ctx context.Context, c *sdk.Client, uuid string) error {
		_, err := c.ArchivePolicyException(ctx, uuid)
		return err
	},
}
