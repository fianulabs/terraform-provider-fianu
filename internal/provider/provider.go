// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

// Package provider hosts the Fianu Terraform provider built on
// terraform-plugin-framework v1.19+. The provider configures a
// github.com/fianulabs/core/v2/external/pkg/sdk/v2 Client and shares it across
// all resources via ProviderData.
package provider

import (
	"context"
	"os"

	sdk "github.com/fianulabs/core/v2/external/pkg/sdk/v2"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	controltest "github.com/fianulabs/terraform-provider-fianu/internal/actions/control_test"
	"github.com/fianulabs/terraform-provider-fianu/internal/resources/control"
)

// Provider config env-var keys. Used as fallbacks when the matching HCL
// attribute is null/empty so existing CI tooling can keep its credentials in
// the environment.
const (
	envHost         = "FIANU_HOST"
	envClientID     = "FIANU_CLIENT_ID"
	envClientSecret = "FIANU_CLIENT_SECRET"
	envTokenURL     = "FIANU_TOKEN_URL"
	envToken        = "FIANU_TOKEN"

	// defaultTokenURL is the production Fianu OIDC token endpoint (Auth0
	// custom domain). Applied when neither `token_url` nor FIANU_TOKEN_URL
	// is set so customers only have to supply `client_id` + `client_secret`
	// against the public console; override only if running against a
	// non-standard IDP or a private console deployment.
	defaultTokenURL = "https://cloudauth.fianu.io/oauth/token"
)

// Compile-time interface checks lock down what the provider must implement.
// ProviderWithActions is the framework's extension point for actions —
// imperative, run-on-demand operations like fianu_control_test.
var (
	_ provider.Provider            = (*fianuProvider)(nil)
	_ provider.ProviderWithActions = (*fianuProvider)(nil)
)

// New returns a provider factory suitable for providerserver.Serve. The
// version string flows into the User-Agent the SDK sends on every request.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &fianuProvider{version: version}
	}
}

type fianuProvider struct {
	version string
}

// fianuProviderModel mirrors the HCL-side provider config.
type fianuProviderModel struct {
	Host         types.String `tfsdk:"host"`
	ClientID     types.String `tfsdk:"client_id"`
	ClientSecret types.String `tfsdk:"client_secret"`
	TokenURL     types.String `tfsdk:"token_url"`
	Token        types.String `tfsdk:"token"`
}

func (p *fianuProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "fianu"
	resp.Version = p.version
}

func (p *fianuProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Fianu provider manages compliance entities (controls, policies, environments, targets, collections) on a Fianu Console deployment.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				MarkdownDescription: "Base URL of the Fianu Console (e.g., `https://console.fianu.io`). Falls back to `FIANU_HOST`.",
				Optional:            true,
			},
			"client_id": schema.StringAttribute{
				MarkdownDescription: "OIDC client ID for the client-credentials grant. Falls back to `FIANU_CLIENT_ID`.",
				Optional:            true,
			},
			"client_secret": schema.StringAttribute{
				MarkdownDescription: "OIDC client secret. Falls back to `FIANU_CLIENT_SECRET`.",
				Optional:            true,
				Sensitive:           true,
			},
			"token_url": schema.StringAttribute{
				MarkdownDescription: "OIDC token endpoint URL. Falls back to `FIANU_TOKEN_URL`, then to `https://cloudauth.fianu.io/oauth/token` (the public Fianu IDP). Override only when running against a private deployment or non-standard IDP.",
				Optional:            true,
			},
			"token": schema.StringAttribute{
				MarkdownDescription: "Pre-issued bearer token, mutually exclusive with the OIDC client-credentials fields. Falls back to `FIANU_TOKEN`. Use this only when you already hold a long-lived token (e.g., a CI service account); the OIDC flow is preferred for everything else.",
				Optional:            true,
				Sensitive:           true,
			},
		},
	}
}

func (p *fianuProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg fianuProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	host := stringOrEnv(cfg.Host, envHost)
	if host == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("host"),
			"missing host",
			"The Fianu provider requires a host URL. Set the `host` attribute or the FIANU_HOST environment variable.",
		)
		return
	}

	opts := []sdk.Opt{sdk.WithBaseURL(host)}

	if tok := stringOrEnv(cfg.Token, envToken); tok != "" {
		opts = append(opts, sdk.WithBearerToken(tok))
	} else {
		clientID := stringOrEnv(cfg.ClientID, envClientID)
		clientSecret := stringOrEnv(cfg.ClientSecret, envClientSecret)
		tokenURL := stringOrEnv(cfg.TokenURL, envTokenURL)
		if tokenURL == "" {
			tokenURL = defaultTokenURL
		}
		if clientID == "" || clientSecret == "" {
			resp.Diagnostics.AddError("authentication misconfigured", errMissingCredentials{}.Error())
			return
		}
		opts = append(opts, sdk.WithOIDC(clientID, clientSecret, tokenURL))
	}

	client, err := sdk.NewClient(opts...)
	if err != nil {
		resp.Diagnostics.AddError("client construction failed", err.Error())
		return
	}

	resp.ResourceData = client
	resp.DataSourceData = client
	resp.EphemeralResourceData = client
	resp.ActionData = client
}

func (p *fianuProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		control.NewResource,
	}
}

func (p *fianuProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	// Data sources land alongside their resources in v0.2; v0.1 ships
	// resource-only.
	return nil
}

// Actions returns the imperative, run-on-demand operations the provider
// exposes. Wired through the ProviderWithActions extension interface
// (terraform-plugin-framework v1.16+, Terraform CLI v1.14+).
func (p *fianuProvider) Actions(_ context.Context) []func() action.Action {
	return []func() action.Action{
		controltest.NewAction,
	}
}

// stringOrEnv resolves the configured value, falling back to the environment
// variable when the attribute is null or empty.
func stringOrEnv(attr types.String, envKey string) string {
	if !attr.IsNull() && !attr.IsUnknown() {
		if v := attr.ValueString(); v != "" {
			return v
		}
	}
	return os.Getenv(envKey)
}

type errMissingCredentials struct{}

func (errMissingCredentials) Error() string {
	return "no credentials configured. Set either `token` (or FIANU_TOKEN) for static-bearer auth, or both `client_id` and `client_secret` (or the matching FIANU_CLIENT_ID/FIANU_CLIENT_SECRET env vars) for OIDC client-credentials auth. `token_url` defaults to https://cloudauth.fianu.io/oauth/token and only needs to be set when overriding the IDP."
}
