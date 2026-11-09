// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/solcreek/terraform-provider-kinsta/internal/kinsta"
)

// Ensure the provider satisfies the framework interface.
var _ provider.Provider = (*kinstaProvider)(nil)

type kinstaProvider struct {
	version string
}

type providerModel struct {
	APIKey         types.String `tfsdk:"api_key"`
	CompanyID      types.String `tfsdk:"company_id"`
	APIURL         types.String `tfsdk:"api_url"`
	TimeoutSeconds types.Int64  `tfsdk:"timeout_seconds"`
}

// providerData is handed to every resource and data source.
type providerData struct {
	client    *kinsta.Client
	companyID string
}

// New returns a provider factory for the given version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &kinstaProvider{version: version}
	}
}

func (p *kinstaProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "kinsta"
	resp.Version = p.version
}

func (p *kinstaProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage Kinsta WordPress sites, environments, site domains and Kinsta DNS records " +
			"through the Kinsta API v2. Authenticates with a Kinsta API key.",
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				MarkdownDescription: "Kinsta API key, created in MyKinsta under *Company settings → API keys*. " +
					"Falls back to the `KINSTA_API_KEY` environment variable.",
				Optional:  true,
				Sensitive: true,
			},
			"company_id": schema.StringAttribute{
				MarkdownDescription: "Company ID that sites, zones and regions are listed for. Falls back to " +
					"`KINSTA_COMPANY_ID`. When neither is set, it is looked up from the API key " +
					"(`GET /validate`), since every key belongs to exactly one company.",
				Optional: true,
			},
			"api_url": schema.StringAttribute{
				MarkdownDescription: "Kinsta API base URL. Defaults to `https://api.kinsta.com/v2`. Falls back to `KINSTA_API_URL`.",
				Optional:            true,
			},
			"timeout_seconds": schema.Int64Attribute{
				MarkdownDescription: "Per-request HTTP timeout in seconds. Defaults to 60. Long-running " +
					"operations (site creation, domain changes) are polled and are not bound by it.",
				Optional: true,
			},
		},
	}
}

func (p *kinstaProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for attr, v := range map[string]types.String{"api_key": cfg.APIKey, "company_id": cfg.CompanyID, "api_url": cfg.APIURL} {
		if v.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root(attr), "Unknown provider configuration",
				fmt.Sprintf("`%s` must be known when the provider is configured; it cannot depend on a value that is only known after apply.", attr))
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	apiKey := firstNonEmpty(cfg.APIKey.ValueString(), os.Getenv("KINSTA_API_KEY"))
	if apiKey == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("api_key"),
			"Missing Kinsta credentials",
			"Set the provider `api_key` argument or the KINSTA_API_KEY environment variable.",
		)
		return
	}

	timeout := 60 * time.Second
	if !cfg.TimeoutSeconds.IsNull() && cfg.TimeoutSeconds.ValueInt64() > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds.ValueInt64()) * time.Second
	}

	client := kinsta.New(apiKey,
		kinsta.WithBaseURL(firstNonEmpty(cfg.APIURL.ValueString(), os.Getenv("KINSTA_API_URL"))),
		kinsta.WithTimeout(timeout),
		kinsta.WithUserAgent("terraform-provider-kinsta/"+p.version),
		kinsta.WithPollInterval(pollInterval()),
	)

	companyID := firstNonEmpty(cfg.CompanyID.ValueString(), os.Getenv("KINSTA_COMPANY_ID"))
	if companyID == "" {
		info, err := client.Validate(ctx)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("company_id"), "Unable to look up the company of the API key",
				"No `company_id` (or KINSTA_COMPANY_ID) is set, so the provider asked the API which company the key belongs to, and that failed: "+err.Error())
			return
		}
		companyID = info.Company
	}

	data := &providerData{client: client, companyID: companyID}
	resp.ResourceData = data
	resp.DataSourceData = data
}

func (p *kinstaProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newSiteResource,
		newEnvironmentResource,
		newSiteDomainResource,
		newPrimaryDomainResource,
		newDNSRecordResource,
	}
}

func (p *kinstaProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newSiteDataSource,
		newSitesDataSource,
		newDNSZoneDataSource,
		newRegionsDataSource,
		newSiteDomainDataSource,
	}
}

// pollInterval is how often long-running operations are polled. Tests shorten
// it through KINSTA_POLL_INTERVAL_MS; it is intentionally not a provider
// argument.
func pollInterval() time.Duration {
	if v := os.Getenv("KINSTA_POLL_INTERVAL_MS"); v != "" {
		var ms int
		if _, err := fmt.Sscanf(v, "%d", &ms); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 5 * time.Second
}

// configureData extracts the provider data in a resource or data source
// Configure method. It returns nil before the provider is configured.
func configureData(raw any, diags *diag.Diagnostics) *providerData {
	if raw == nil {
		return nil
	}
	d, ok := raw.(*providerData)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("Expected *providerData, got %T. Please report this issue to the provider developers.", raw))
		return nil
	}
	return d
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
