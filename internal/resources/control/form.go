// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package control

import (
	fianu_types "github.com/fianulabs/core/v2/external/db/types/fianu"
	db_vars "github.com/fianulabs/core/v2/external/db/variables"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// formRefModel is a reference to a fianu_form, not a form definition — the
// questionnaire itself is its own entity. Server-side this is a fianu.XEntity;
// only the key is authored here because the deploy resolves the rest
// (pkg/controls/db.go::executeCreate fetches the form's active version and
// fills in name + version UUID before the association row is written).
type formRefModel struct {
	Path types.String `tfsdk:"path"`
	UUID types.String `tfsdk:"uuid"`
}

func formAttribute() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: "Attestation form for this control — a reference to a `fianu_form`, which supplies the questionnaire an attester fills in. Give exactly one of `path` or `uuid`.\n\nRemoving this block does **not** detach the form: a deploy omitting the section means \"unchanged\" server-side, and the association carries forward to the new control version. Detach in the Console.",
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"path": schema.StringAttribute{
				MarkdownDescription: "Path of the form, e.g. `fianu_form.vendor_review.path`. Resolved to the form's current active version at deploy time.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.ExactlyOneOf(path.MatchRelative().AtParent().AtName("uuid")),
				},
			},
			"uuid": schema.StringAttribute{
				MarkdownDescription: "Entity UUID of the form, e.g. `fianu_form.vendor_review.uuid`. Alternative to `path`.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
		},
	}
}

// buildFormRef converts the HCL reference to the wire shape. Type is set
// explicitly: XEntity.Resolve picks its resolver from that field, so an unset
// Type resolves the key against the wrong entity table.
func buildFormRef(in *formRefModel) *fianu_types.XEntity {
	if in == nil {
		return nil
	}
	ref := &fianu_types.XEntity{
		UUID: in.UUID.ValueString(),
		Type: db_vars.EntityTypeForm,
	}
	if p := in.Path.ValueString(); p != "" {
		ref.Path = &p
	}
	return ref
}
