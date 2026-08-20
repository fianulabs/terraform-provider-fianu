// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package entity

import (
	"context"

	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	sdk "github.com/fianulabs/core/v2/external/pkg/sdk/v2"

	"github.com/fianulabs/terraform-provider-fianu/internal/resources/base"
)

// kind is one entity type's worth of data-source configuration. Every field
// that differs between `data.fianu_platform` and `data.fianu_collection`
// lives here; everything else is shared by dataSource.
//
// This mirrors internal/resources/policy/kind.go, which uses the same shape to
// render two resources (`fianu_policy`, `fianu_policy_exception`) off one
// implementation.
type kind struct {
	// typeNameSuffix is appended to the provider prefix — `fianu_<suffix>`.
	typeNameSuffix string

	// entityType is the composite ID prefix (`<entity_type>/<key>`). It is
	// usually the same word as typeNameSuffix, but not always: report
	// templates are `template` on the wire and `report_template` in HCL.
	entityType string

	// description is the data source's MarkdownDescription. tfplugindocs
	// renders it as the page intro.
	description string

	// fetch performs the read and flattens whatever the SDK returns into the
	// shared envelope.
	//
	// It takes entityType rather than closing over the literal so the string
	// is declared exactly once per kind — a mismatch between the struct field
	// and the closure would produce an `id` that ParseID rejects on import,
	// with nothing to catch it at compile time.
	fetch func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error)
}

// kinds is every entity the console exposes a canonical single-entity read
// for. The pod-backed resources (`fianu_entity_pod`, `fianu_notification`)
// are deliberately absent: pods are rows keyed by (entity_id, pod_type, key),
// not entities — no envelope, no path, nothing for a lookup-by-path data
// source to return.
var kinds = []kind{
	{
		typeNameSuffix: "collection",
		entityType:     "collection",
		description:    "Looks up an existing Fianu collection by path. Use this to reference a collection this configuration does not manage — for example one created through the console or `fianu console deploy`.",
		fetch: func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error) {
			e, err := c.FetchCollection(ctx, key, nil)
			if err != nil {
				return base.EntityEnvelope{}, err
			}
			return base.EnvelopeFromStandardEntity(entityType, &e.StandardEntity), nil
		},
	},
	{
		typeNameSuffix: "control",
		entityType:     "control",
		description:    "Looks up an existing Fianu control by path.",
		fetch: func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error) {
			e, err := c.FetchControl(ctx, key, nil, nil)
			if err != nil {
				return base.EntityEnvelope{}, err
			}
			return base.EnvelopeFromStandardEntity(entityType, &e.StandardEntity), nil
		},
	},
	{
		typeNameSuffix: "environment",
		entityType:     "environment",
		description:    "Looks up an existing Fianu environment by path.",
		fetch: func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error) {
			// entities.Environment is a type alias for
			// StandardEntity[EnvironmentDetail], so the fetched value is
			// already the envelope — no embedded field to reach through.
			e, err := c.FetchEnvironment(ctx, key, nil)
			if err != nil {
				return base.EntityEnvelope{}, err
			}
			return base.EnvelopeFromStandardEntity(entityType, e), nil
		},
	},
	{
		typeNameSuffix: "form",
		entityType:     "form",
		description:    "Looks up an existing Fianu form by path.",
		fetch: func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error) {
			e, err := c.FetchForm(ctx, key, nil, nil)
			if err != nil {
				return base.EntityEnvelope{}, err
			}
			return base.EnvelopeFromStandardEntity(entityType, &e.StandardEntity), nil
		},
	},
	{
		typeNameSuffix: "gate",
		entityType:     "gate",
		description:    "Looks up an existing Fianu gate by path.",
		fetch: func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error) {
			// FetchGate returns *entities.Control: a gate is a control with
			// entity type `gate`, same as the resource side.
			e, err := c.FetchGate(ctx, key, nil)
			if err != nil {
				return base.EntityEnvelope{}, err
			}
			return base.EnvelopeFromStandardEntity(entityType, &e.StandardEntity), nil
		},
	},
	{
		typeNameSuffix: "index",
		entityType:     "index",
		description:    "Looks up an existing Fianu index by path.",
		fetch: func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error) {
			// Indexes are driven through the typed /entities/indexes API
			// rather than the multipart deploy route, so the read is GetIndex
			// and the return type carries an operational ComputeState the
			// envelope ignores.
			e, err := c.GetIndex(ctx, key, nil, nil, nil)
			if err != nil {
				return base.EntityEnvelope{}, err
			}
			return base.EnvelopeFromStandardEntity(entityType, &e.StandardEntity), nil
		},
	},
	{
		typeNameSuffix: "instance",
		entityType:     "instance",
		description:    "Looks up an existing Fianu integration instance by path.",
		fetch: func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error) {
			e, err := c.FetchInstance(ctx, key, nil)
			if err != nil {
				return base.EntityEnvelope{}, err
			}
			return base.EnvelopeFromStandardEntity(entityType, &e.StandardEntity), nil
		},
	},
	{
		typeNameSuffix: "platform",
		entityType:     "platform",
		description:    "Looks up an existing Fianu platform by path. The common use is resolving a platform's `uuid` for `fianu_instance`'s `detail.platform_uuid` when the platform is not managed by this configuration.",
		fetch: func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error) {
			e, err := c.FetchPlatform(ctx, key)
			if err != nil {
				return base.EntityEnvelope{}, err
			}
			return base.EnvelopeFromStandardEntity(entityType, &e.StandardEntity), nil
		},
	},
	{
		typeNameSuffix: "policy",
		entityType:     "policy",
		description:    "Looks up an existing Fianu policy by path.",
		fetch: func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error) {
			e, err := c.FetchPolicy(ctx, key, nil, nil)
			if err != nil {
				return base.EntityEnvelope{}, err
			}
			return base.EnvelopeFromStandardEntity(entityType, &e.StandardEntity), nil
		},
	},
	{
		typeNameSuffix: "policy_exception",
		entityType:     "policy_exception",
		description:    "Looks up an existing Fianu policy exception by path.",
		fetch: func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error) {
			// Exceptions are policies with detail.type = exception, but they
			// are read through their own route: FetchPolicy filters on
			// entityType=policy and 404s on an exception.
			e, err := c.FetchPolicyException(ctx, key, nil)
			if err != nil {
				return base.EntityEnvelope{}, err
			}
			return base.EnvelopeFromStandardEntity(entityType, &e.StandardEntity), nil
		},
	},
	{
		typeNameSuffix: "report_template",
		entityType:     string(db_vars.EntityTypeReportTemplate),
		description:    "Looks up an existing Fianu report template by path.",
		fetch: func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error) {
			// entities.ReportTemplate is a type alias, like Environment.
			e, err := c.FetchTemplate(ctx, key, nil)
			if err != nil {
				return base.EntityEnvelope{}, err
			}
			return base.EnvelopeFromStandardEntity(entityType, e), nil
		},
	},
	{
		typeNameSuffix: "target",
		entityType:     "target",
		description:    "Looks up an existing Fianu target by path.",
		fetch: func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error) {
			// TargetWithEnvironment -> Target -> StandardEntity: two levels of
			// embedding, one unambiguous promotion. The satellite fields
			// (environments, aliases, documentation) are not envelope data.
			e, err := c.FetchTarget(ctx, key, nil)
			if err != nil {
				return base.EntityEnvelope{}, err
			}
			return base.EnvelopeFromStandardEntity(entityType, &e.StandardEntity), nil
		},
	},
	{
		typeNameSuffix: "tool",
		entityType:     "tool",
		description:    "Looks up an existing Fianu tool by path.",
		fetch: func(ctx context.Context, c *sdk.Client, entityType, key string) (base.EntityEnvelope, error) {
			e, err := c.FetchTool(ctx, key)
			if err != nil {
				return base.EntityEnvelope{}, err
			}
			return base.EnvelopeFromStandardEntity(entityType, &e.StandardEntity), nil
		},
	},
}
