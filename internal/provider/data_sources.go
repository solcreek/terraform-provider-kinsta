// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/solcreek/terraform-provider-kinsta/internal/kinsta"
)

// baseDataSource holds the provider data every data source needs.
type baseDataSource struct {
	data *providerData
}

func (d *baseDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureData(req.ProviderData, &resp.Diagnostics)
}

func environmentsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "The site's environments.",
		Computed:            true,
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Computed: true, MarkdownDescription: "Environment ID."},
			"name":         schema.StringAttribute{Computed: true, MarkdownDescription: "Environment name, e.g. `live` or `staging`."},
			"display_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Environment display name."},
			"is_premium":   schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether it is a premium environment."},
		}},
	}
}

// ---------------------------------------------------------------- kinsta_site

type siteDataSource struct{ baseDataSource }

func newSiteDataSource() datasource.DataSource { return &siteDataSource{} }

type siteDataModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	DisplayName       types.String `tfsdk:"display_name"`
	Status            types.String `tfsdk:"status"`
	CompanyID         types.String `tfsdk:"company_id"`
	LiveEnvironmentID types.String `tfsdk:"live_environment_id"`
	PrimaryDomain     types.String `tfsdk:"primary_domain"`
	Environments      types.List   `tfsdk:"environments"`
}

func (d *siteDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_site"
}

func (d *siteDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a WordPress site by ID, or by its name or display name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Site ID. Set this or `name`.",
				Optional:            true,
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Site name or display name (case-insensitive, exact). Set this or `id`.",
				Optional:            true,
				Computed:            true,
			},
			"display_name":        schema.StringAttribute{Computed: true, MarkdownDescription: "Display name shown in MyKinsta."},
			"status":              schema.StringAttribute{Computed: true, MarkdownDescription: "Site status, e.g. `live`."},
			"company_id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Company the site belongs to."},
			"live_environment_id": schema.StringAttribute{Computed: true, MarkdownDescription: "ID of the live environment."},
			"primary_domain":      schema.StringAttribute{Computed: true, MarkdownDescription: "Primary domain of the live environment."},
			"environments":        environmentsAttribute(),
		},
	}
}

func (d *siteDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *siteDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg siteDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := cfg.ID.ValueString()
	if id == "" {
		sites, err := d.data.client.ListSites(ctx, d.data.companyID)
		if err != nil {
			resp.Diagnostics.AddError("Unable to list sites", err.Error())
			return
		}
		want := cfg.Name.ValueString()
		var matches []kinsta.Site
		for _, s := range sites {
			if strings.EqualFold(s.Name, want) || strings.EqualFold(s.DisplayName, want) {
				matches = append(matches, s)
			}
		}
		switch len(matches) {
		case 0:
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Site not found",
				fmt.Sprintf("No site of company %s is named %q.", d.data.companyID, want))
			return
		case 1:
			id = matches[0].ID
		default:
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Ambiguous site name",
				fmt.Sprintf("%d sites match %q; look the site up by id instead.", len(matches), want))
			return
		}
	}

	site, err := d.data.client.GetSite(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read site", err.Error())
		return
	}
	out := siteDataModel{
		ID:                types.StringValue(site.ID),
		Name:              types.StringValue(site.Name),
		DisplayName:       types.StringValue(site.DisplayName),
		Status:            types.StringValue(site.Status),
		CompanyID:         types.StringValue(site.CompanyID),
		LiveEnvironmentID: types.StringNull(),
		PrimaryDomain:     types.StringNull(),
	}
	if live := site.LiveEnvironment(); live != nil {
		out.LiveEnvironmentID = types.StringValue(live.ID)
		if live.PrimaryDomain != nil {
			out.PrimaryDomain = types.StringValue(kinsta.NormalizeName(live.PrimaryDomain.Name))
		}
	}
	envs, diags := environmentSummaries(site.Environments)
	resp.Diagnostics.Append(diags...)
	out.Environments = envs
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}

// --------------------------------------------------------------- kinsta_sites

type sitesDataSource struct{ baseDataSource }

func newSitesDataSource() datasource.DataSource { return &sitesDataSource{} }

var siteSummaryAttrTypes = map[string]attr.Type{
	"id":                  types.StringType,
	"name":                types.StringType,
	"display_name":        types.StringType,
	"status":              types.StringType,
	"live_environment_id": types.StringType,
	"primary_domain":      types.StringType,
}

func (d *sitesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sites"
}

func (d *sitesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists every WordPress site of the company.",
		Attributes: map[string]schema.Attribute{
			"sites": schema.ListNestedAttribute{
				MarkdownDescription: "The company's sites.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":                  schema.StringAttribute{Computed: true, MarkdownDescription: "Site ID."},
					"name":                schema.StringAttribute{Computed: true, MarkdownDescription: "Site name."},
					"display_name":        schema.StringAttribute{Computed: true, MarkdownDescription: "Display name."},
					"status":              schema.StringAttribute{Computed: true, MarkdownDescription: "Site status."},
					"live_environment_id": schema.StringAttribute{Computed: true, MarkdownDescription: "ID of the live environment."},
					"primary_domain":      schema.StringAttribute{Computed: true, MarkdownDescription: "Primary domain of the live environment."},
				}},
			},
		},
	}
}

func (d *sitesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	sites, err := d.data.client.ListSites(ctx, d.data.companyID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list sites", err.Error())
		return
	}
	elems := make([]attr.Value, 0, len(sites))
	for _, s := range sites {
		liveID, primary := types.StringNull(), types.StringNull()
		if live := s.LiveEnvironment(); live != nil {
			liveID = types.StringValue(live.ID)
			if live.PrimaryDomain != nil {
				primary = types.StringValue(kinsta.NormalizeName(live.PrimaryDomain.Name))
			}
		}
		obj, diags := types.ObjectValue(siteSummaryAttrTypes, map[string]attr.Value{
			"id":                  types.StringValue(s.ID),
			"name":                types.StringValue(s.Name),
			"display_name":        types.StringValue(s.DisplayName),
			"status":              types.StringValue(s.Status),
			"live_environment_id": liveID,
			"primary_domain":      primary,
		})
		resp.Diagnostics.Append(diags...)
		elems = append(elems, obj)
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: siteSummaryAttrTypes}, elems)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("sites"), list)...)
}

// ------------------------------------------------------------ kinsta_dns_zone

type dnsZoneDataSource struct{ baseDataSource }

func newDNSZoneDataSource() datasource.DataSource { return &dnsZoneDataSource{} }

type dnsZoneModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	IsActive types.Bool   `tfsdk:"is_active"`
	SiteID   types.String `tfsdk:"site_id"`
}

func (d *dnsZoneDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}

func (d *dnsZoneDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a zone on Kinsta DNS by name.",
		Attributes: map[string]schema.Attribute{
			"name":      schema.StringAttribute{Required: true, MarkdownDescription: "Zone name, e.g. `example.com`."},
			"id":        schema.StringAttribute{Computed: true, MarkdownDescription: "Zone ID, for `kinsta_dns_record.zone_id`."},
			"is_active": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the zone is active."},
			"site_id":   schema.StringAttribute{Computed: true, MarkdownDescription: "Site the zone is linked to, if any."},
		},
	}
}

func (d *dnsZoneDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg dnsZoneModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	zones, err := d.data.client.ListZones(ctx, d.data.companyID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list Kinsta DNS zones", err.Error())
		return
	}
	want := kinsta.NormalizeName(cfg.Name.ValueString())
	for _, z := range zones {
		if kinsta.NormalizeName(z.Name) != want {
			continue
		}
		cfg.ID = types.StringValue(z.ID)
		cfg.IsActive = types.BoolValue(z.IsActive)
		cfg.SiteID = types.StringPointerValue(z.SiteID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
		return
	}
	hint := ""
	for _, z := range zones {
		if strings.HasSuffix(want, "."+kinsta.NormalizeName(z.Name)) {
			hint = fmt.Sprintf(" Did you mean the zone %s?", z.Name)
			break
		}
	}
	resp.Diagnostics.AddAttributeError(path.Root("name"), "Zone not found",
		fmt.Sprintf("%s is not a Kinsta DNS zone of company %s.%s", want, d.data.companyID, hint))
}

// ------------------------------------------------------------- kinsta_regions

type regionsDataSource struct{ baseDataSource }

func newRegionsDataSource() datasource.DataSource { return &regionsDataSource{} }

var regionAttrTypes = map[string]attr.Type{"region": types.StringType, "name": types.StringType}

func (d *regionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_regions"
}

func (d *regionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the data center regions the company can create sites in.",
		Attributes: map[string]schema.Attribute{
			"regions": schema.ListNestedAttribute{
				MarkdownDescription: "Available regions.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"region": schema.StringAttribute{Computed: true, MarkdownDescription: "Region code for `kinsta_site.region`, e.g. `us-ashburn-1`."},
					"name":   schema.StringAttribute{Computed: true, MarkdownDescription: "Human-readable name, e.g. `Ashburn (US)`."},
				}},
			},
		},
	}
}

func (d *regionsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	regions, err := d.data.client.ListRegions(ctx, d.data.companyID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list regions", err.Error())
		return
	}
	elems := make([]attr.Value, 0, len(regions))
	for _, r := range regions {
		obj, diags := types.ObjectValue(regionAttrTypes, map[string]attr.Value{
			"region": types.StringValue(r.Region), "name": types.StringValue(r.Name),
		})
		resp.Diagnostics.Append(diags...)
		elems = append(elems, obj)
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: regionAttrTypes}, elems)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("regions"), list)...)
}

// --------------------------------------------------------- kinsta_site_domain

type siteDomainDataSource struct{ baseDataSource }

func newSiteDomainDataSource() datasource.DataSource { return &siteDomainDataSource{} }

func (d *siteDomainDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_site_domain"
}

func (d *siteDomainDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	hint := schema.ListNestedAttribute{
		Computed: true,
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"type":  schema.StringAttribute{Computed: true, MarkdownDescription: "Record type."},
			"name":  schema.StringAttribute{Computed: true, MarkdownDescription: "Record name."},
			"value": schema.StringAttribute{Computed: true, MarkdownDescription: "Record value."},
		}},
	}
	verification, pointing := hint, hint
	verification.MarkdownDescription = "DNS records Kinsta needs to verify ownership of the domain. Empty once the domain is verified."
	pointing.MarkdownDescription = "DNS records that point the domain at the site. Empty once the domain is verified."
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a domain of an environment and the DNS records Kinsta still asks for.",
		Attributes: map[string]schema.Attribute{
			"environment_id":       schema.StringAttribute{Required: true, MarkdownDescription: "Environment ID."},
			"domain_name":          schema.StringAttribute{Required: true, MarkdownDescription: "Domain name."},
			"id":                   schema.StringAttribute{Computed: true, MarkdownDescription: "Site domain ID."},
			"uses_cloudflare_dns":  schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether Kinsta reports the domain as using Cloudflare DNS."},
			"verification_records": verification,
			"pointing_records":     pointing,
		},
	}
}

func (d *siteDomainDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg siteDomainModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := d.data.client.FindSiteDomain(ctx, cfg.EnvironmentID.ValueString(), cfg.DomainName.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to list site domains", err.Error())
		return
	}
	if found == nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_name"), "Domain not found",
			fmt.Sprintf("%s is not a domain of environment %s.", cfg.DomainName.ValueString(), cfg.EnvironmentID.ValueString()))
		return
	}
	cfg.ID = types.StringValue(found.ID)
	cfg.UsesCloudflareDNS = types.BoolValue(found.UsesCloudflareDNS)
	records, err := d.data.client.GetSiteDomainRecords(ctx, found.ID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the domain's DNS records", err.Error())
		return
	}
	var diags = cfg.setRecords(ctx, records)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
