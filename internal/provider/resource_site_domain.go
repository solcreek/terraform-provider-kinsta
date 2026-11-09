// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/solcreek/terraform-provider-kinsta/internal/kinsta"
)

var (
	_ resource.Resource                = (*siteDomainResource)(nil)
	_ resource.ResourceWithConfigure   = (*siteDomainResource)(nil)
	_ resource.ResourceWithImportState = (*siteDomainResource)(nil)
)

// domainNamePattern accepts a lowercase host name without a trailing dot.
var domainNamePattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9-]{2,}$`)

type siteDomainResource struct {
	data *providerData
}

func newSiteDomainResource() resource.Resource { return &siteDomainResource{} }

type siteDomainModel struct {
	ID                  types.String `tfsdk:"id"`
	EnvironmentID       types.String `tfsdk:"environment_id"`
	DomainName          types.String `tfsdk:"domain_name"`
	UsesCloudflareDNS   types.Bool   `tfsdk:"uses_cloudflare_dns"`
	VerificationRecords types.List   `tfsdk:"verification_records"`
	PointingRecords     types.List   `tfsdk:"pointing_records"`
}

func (r *siteDomainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_site_domain"
}

func siteDomainRecordAttributes() map[string]schema.Attribute {
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
	return map[string]schema.Attribute{"verification_records": verification, "pointing_records": pointing}
}

func (r *siteDomainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			MarkdownDescription: "Site domain ID.",
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"environment_id": schema.StringAttribute{
			MarkdownDescription: "ID of the environment the domain is added to, e.g. `kinsta_site.example.live_environment_id`.",
			Required:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"domain_name": schema.StringAttribute{
			MarkdownDescription: "Domain to add, lowercase and without a trailing dot, e.g. `www.example.com`. " +
				"It is added without a wildcard; add `www.` and other hosts as domains of their own.",
			Required:      true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			Validators: []validator.String{
				stringvalidator.RegexMatches(domainNamePattern, "must be a lowercase domain name without a trailing dot"),
			},
		},
		"uses_cloudflare_dns": schema.BoolAttribute{
			MarkdownDescription: "Whether Kinsta reports the domain as using Cloudflare DNS.",
			Computed:            true,
		},
	}
	for k, v := range siteDomainRecordAttributes() {
		attrs[k] = v
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds a domain to a WordPress environment. Kinsta then asks for DNS records to verify " +
			"the domain and point it at the site; they are exported as `verification_records` and " +
			"`pointing_records` so they can be created with any DNS provider (or `kinsta_dns_record`).",
		Attributes: attrs,
	}
}

func (r *siteDomainResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureData(req.ProviderData, &resp.Diagnostics)
}

func (r *siteDomainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan siteDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, domainOperationTimeout)
	defer cancel()

	envID, name := plan.EnvironmentID.ValueString(), plan.DomainName.ValueString()
	existing, err := r.data.client.FindSiteDomain(ctx, envID, name)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list site domains", err.Error())
		return
	}
	if existing != nil {
		resp.Diagnostics.AddError("Domain already on the environment",
			fmt.Sprintf("%s is already a domain of environment %s. Import it instead: terraform import <address> %s/%s", name, envID, envID, name))
		return
	}

	if err := r.data.client.AddSiteDomain(ctx, envID, name); err != nil {
		resp.Diagnostics.AddError("Unable to add site domain", err.Error())
		return
	}
	found, err := r.data.client.FindSiteDomain(ctx, envID, name)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list site domains", err.Error())
		return
	}
	if found == nil {
		resp.Diagnostics.AddError("Site domain not found after adding it",
			fmt.Sprintf("Kinsta finished adding %s but it is not listed on environment %s. Check MyKinsta.", name, envID))
		return
	}
	resp.Diagnostics.Append(r.fill(ctx, &plan, found)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *siteDomainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state siteDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	domains, err := r.data.client.ListSiteDomains(ctx, state.EnvironmentID.ValueString())
	if err != nil {
		if kinsta.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to list site domains", err.Error())
		return
	}
	found := matchSiteDomain(domains, state.ID.ValueString(), state.DomainName.ValueString())
	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(r.fill(ctx, &state, found)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every configurable attribute requires replacement.
func (r *siteDomainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan siteDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *siteDomainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state siteDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, domainOperationTimeout)
	defer cancel()

	envID := state.EnvironmentID.ValueString()
	domains, err := r.data.client.ListSiteDomains(ctx, envID)
	if err != nil {
		if kinsta.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to list site domains", err.Error())
		return
	}
	found := matchSiteDomain(domains, state.ID.ValueString(), state.DomainName.ValueString())
	if found == nil {
		return
	}
	if err := r.data.client.DeleteSiteDomains(ctx, envID, found.ID); err != nil {
		resp.Diagnostics.AddError("Unable to remove site domain", err.Error())
		return
	}
	if still, err := r.data.client.FindSiteDomain(ctx, envID, found.Name); err != nil {
		resp.Diagnostics.AddError("Unable to confirm the site domain was removed", err.Error())
	} else if still != nil {
		resp.Diagnostics.AddError("Site domain still present",
			fmt.Sprintf("Kinsta finished removing %s but it is still listed on environment %s. "+
				"A primary domain cannot be removed; make another domain primary first.", found.Name, envID))
	}
}

func (r *siteDomainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	envID, name, ok := strings.Cut(req.ID, "/")
	if !ok || envID == "" || name == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected `<environment_id>/<domain_name>`, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("environment_id"), envID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("domain_name"), kinsta.NormalizeName(name))...)
}

func (r *siteDomainResource) fill(ctx context.Context, m *siteDomainModel, d *kinsta.SiteDomain) (diags diag.Diagnostics) {
	m.ID = types.StringValue(d.ID)
	m.DomainName = types.StringValue(kinsta.NormalizeName(d.Name))
	m.UsesCloudflareDNS = types.BoolValue(d.UsesCloudflareDNS)
	records, err := r.data.client.GetSiteDomainRecords(ctx, d.ID)
	if err != nil {
		diags.AddError("Unable to read the domain's DNS records", err.Error())
		return diags
	}
	return m.setRecords(ctx, records)
}

func (m *siteDomainModel) setRecords(ctx context.Context, records *kinsta.SiteDomainRecords) (diags diag.Diagnostics) {
	var d diag.Diagnostics
	m.VerificationRecords, d = recordHintsValue(ctx, records.Verification)
	diags.Append(d...)
	m.PointingRecords, d = recordHintsValue(ctx, records.Pointing)
	diags.Append(d...)
	return diags
}

// matchSiteDomain finds a domain by id, falling back to its name (an import
// only knows the name).
func matchSiteDomain(domains []kinsta.SiteDomain, id, name string) *kinsta.SiteDomain {
	for i := range domains {
		if id != "" && domains[i].ID == id {
			return &domains[i]
		}
	}
	want := kinsta.NormalizeName(name)
	for i := range domains {
		if kinsta.NormalizeName(domains[i].Name) == want {
			return &domains[i]
		}
	}
	return nil
}
