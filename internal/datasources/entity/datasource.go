// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package entity implements the read-only `data.fianu_<entity>` lookups.
//
// A data source reads an entity this configuration does not own. The motivating
// case is a cross-reference: `fianu_instance.detail.platform_uuid` needs a
// platform's UUID, and `fianu_platform.jira.uuid` only works when the same
// configuration creates the platform. When the platform predates Terraform,
// lives in another state file, or belongs to another team, the alternatives
// were hardcoding an opaque UUID or plumbing `terraform_remote_state`. Neither
// detects drift; a hardcoded UUID silently points at nothing once the entity is
// archived and recreated.
//
// Lookup is by `path`, not UUID. The path is the identifier humans author and
// `fianu console deploy` keys on; the UUID is what downstream attributes want.
// Path-in/UUID-out is the conversion that removes the hardcoding.
//
// Every entity shares one implementation parameterised by `kind`, because the
// exposed surface is the envelope and nothing else. Detail sections are
// deliberately not exposed — the same rule the resources follow, where the
// server canonicalises and reorders Detail and hydrating it would surface as
// spurious drift. A data source has no state to drift against, but the schema
// cost is real and nobody has asked for it yet.
package entity

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	sdk "github.com/fianulabs/core/v2/external/pkg/sdk/v2"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/fianulabs/terraform-provider-fianu/internal/resources/base"
)

// Compile-time interface checks.
var (
	_ datasource.DataSource              = (*dataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*dataSource)(nil)
)

// dataSourceModel is envelope-only. Unlike the resource models there is no
// Detail field, so base.EnvelopeModel is used directly rather than embedded.
type dataSourceModel = base.EnvelopeModel

type dataSource struct {
	kind   kind
	client *sdk.Client
}

// DataSources returns a factory per entity kind. The provider registers the
// whole slice, so adding an entry to `kinds` ships a new data source with no
// other edit.
func DataSources() []func() datasource.DataSource {
	out := make([]func() datasource.DataSource, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, func() datasource.DataSource { return &dataSource{kind: k} })
	}
	return out
}

func (d *dataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.kind.typeNameSuffix
}

func (d *dataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: d.kind.description,
		Attributes:          base.EnvelopeDataSourceAttributes(),
	}
}

func (d *dataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*sdk.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"unexpected provider data type",
			fmt.Sprintf("expected *sdk.Client, got %T. This is a bug in the provider.", req.ProviderData),
		)
		return
	}
	d.client = client
}

func (d *dataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config dataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key := config.Path.ValueString()
	env, err := d.kind.fetch(ctx, d.client, d.kind.entityType, key)
	if err != nil {
		// A missing entity is an error here, not an eviction. On a resource a
		// 404 means "it was deleted out from under us, drop it from state and
		// recreate"; on a data source it means the configuration references
		// something that does not exist, and continuing would feed an empty
		// UUID to whatever depends on it.
		var apiErr *sdk.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			resp.Diagnostics.AddError(
				fmt.Sprintf("%s %q not found", d.kind.typeNameSuffix, key),
				fmt.Sprintf("No %s exists at path %q. Check the path, or confirm the entity has not been archived.", d.kind.typeNameSuffix, key),
			)
			return
		}
		resp.Diagnostics.AddError(fmt.Sprintf("fetch %s failed", d.kind.typeNameSuffix), err.Error())
		return
	}

	resp.Diagnostics.Append(config.Hydrate(ctx, env)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
