// Package provider hosts the Fianu Terraform provider built on
// terraform-plugin-framework v1.19+. The provider configures a
// github.com/fianulabs/core/v2/external/pkg/clients/fianu Client and shares
// it across all resources via ProviderData.
package provider

import (
	"context"
	"net/url"
	"os"

	fianu "github.com/fianulabs/core/v2/external/pkg/clients/fianu"
	"github.com/fianulabs/core/v2/external/pkg/connections"
	controltest "github.com/fianulabs/terraform-provider-fianu/internal/actions/control_test"
	"github.com/fianulabs/terraform-provider-fianu/internal/resources/control"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
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
				MarkdownDescription: "OIDC token endpoint URL. Falls back to `FIANU_TOKEN_URL`.",
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

	hostURL, err := url.Parse(host)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			path.Root("host"),
			"invalid host URL",
			"Could not parse host as a URL: "+err.Error(),
		)
		return
	}

	auth, err := buildAuthenticator(cfg)
	if err != nil {
		resp.Diagnostics.AddError("authentication misconfigured", err.Error())
		return
	}

	sdk := fianu.NewClient(
		fianu.WithConsole(connections.NewBase(hostURL)),
		fianu.WithAuth(auth),
	)

	resp.ResourceData = sdk
	resp.DataSourceData = sdk
	resp.EphemeralResourceData = sdk
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

// buildAuthenticator picks an Authenticator based on what the user supplied.
// Precedence: explicit token > OIDC client-credentials. Anything else is a
// configuration error — the SDK's default permission-token mode is reserved
// for in-cluster service-to-service callers and would silently fail against
// the public API gateway.
func buildAuthenticator(cfg fianuProviderModel) (fianu.Authenticator, error) {
	if tok := stringOrEnv(cfg.Token, envToken); tok != "" {
		return fianu.NewBearerAuth(tok), nil
	}

	clientID := stringOrEnv(cfg.ClientID, envClientID)
	clientSecret := stringOrEnv(cfg.ClientSecret, envClientSecret)
	tokenURL := stringOrEnv(cfg.TokenURL, envTokenURL)

	if clientID == "" || clientSecret == "" || tokenURL == "" {
		return nil, errMissingCredentials{}
	}

	return fianu.NewOIDCAuth(fianu.OIDCAuthConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenURL,
	}), nil
}

type errMissingCredentials struct{}

func (errMissingCredentials) Error() string {
	return "no credentials configured. Set either `token` (or FIANU_TOKEN) for static-bearer auth, or all three of `client_id`/`client_secret`/`token_url` (or the matching FIANU_CLIENT_ID/FIANU_CLIENT_SECRET/FIANU_TOKEN_URL env vars) for OIDC client-credentials auth."
}
