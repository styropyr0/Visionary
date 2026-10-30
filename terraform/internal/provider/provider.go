// Package provider is the Terraform provider for Visionary: alert rules,
// channels and silences, uptime checks, dashboards and API keys, managed
// through Visionary's HTTP API with an editor or admin API key.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = (*visionaryProvider)(nil)

type visionaryProvider struct {
	version string
}

// New returns the provider factory for providerserver.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &visionaryProvider{version: version} }
}

type providerModel struct {
	URL    types.String `tfsdk:"url"`
	APIKey types.String `tfsdk:"api_key"`
}

func (p *visionaryProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "visionary"
	resp.Version = p.version
}

func (p *visionaryProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage Visionary alert rules, notification channels, silences, uptime checks, dashboards and API keys as code.",
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{
				Description: "Visionary's address, such as https://visionary.example.com. Defaults to the VISIONARY_URL environment variable.",
				Optional:    true,
			},
			"api_key": schema.StringAttribute{
				Description: "An API key with the editor scope (or admin, to manage API keys) from Settings → API keys. Defaults to the VISIONARY_API_KEY environment variable.",
				Optional:    true,
				Sensitive:   true,
			},
		},
	}
}

func (p *visionaryProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.URL.IsUnknown() || cfg.APIKey.IsUnknown() {
		// Known only after another resource is applied; resources wait for it.
		return
	}
	url, key := os.Getenv("VISIONARY_URL"), os.Getenv("VISIONARY_API_KEY")
	if !cfg.URL.IsNull() {
		url = cfg.URL.ValueString()
	}
	if !cfg.APIKey.IsNull() {
		key = cfg.APIKey.ValueString()
	}
	if url == "" {
		resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing Visionary URL",
			"Set url in the provider block or the VISIONARY_URL environment variable.")
	}
	if key == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing API key",
			"Create an editor or admin key in Settings → API keys, then set api_key or VISIONARY_API_KEY.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	c := newClient(url, key, p.version)
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *visionaryProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newAlertRuleResource,
		newAlertChannelResource,
		newAlertSilenceResource,
		newUptimeCheckResource,
		newDashboardResource,
		newAPIKeyResource,
	}
}

func (p *visionaryProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newServicesDataSource,
		newAlertChannelDataSource,
	}
}
