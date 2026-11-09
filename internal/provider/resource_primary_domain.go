// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/solcreek/terraform-provider-kinsta/internal/kinsta"
)

var (
	_ resource.Resource                = (*primaryDomainResource)(nil)
	_ resource.ResourceWithConfigure   = (*primaryDomainResource)(nil)
	_ resource.ResourceWithImportState = (*primaryDomainResource)(nil)
)

type primaryDomainResource struct {
	data *providerData
}

func newPrimaryDomainResource() resource.Resource { return &primaryDomainResource{} }

type primaryDomainModel struct {
	ID                  types.String `tfsdk:"id"`
	SiteID              types.String `tfsdk:"site_id"`
	EnvironmentID       types.String `tfsdk:"environment_id"`
	DomainName          types.String `tfsdk:"domain_name"`
	RunSearchAndReplace types.Bool   `tfsdk:"run_search_and_replace"`
}

func (r *primaryDomainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_primary_domain"
}

func (r *primaryDomainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets an environment's primary domain. The domain must already be a domain of the " +
			"environment (see `kinsta_site_domain`). Destroying this resource leaves the primary domain as it is " +
			"and only removes it from Terraform state, since an environment always has a primary domain.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Same as `environment_id`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"site_id": schema.StringAttribute{
				MarkdownDescription: "ID of the site the environment belongs to.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"environment_id": schema.StringAttribute{
				MarkdownDescription: "ID of the environment.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"domain_name": schema.StringAttribute{
				MarkdownDescription: "Domain to make primary, lowercase and without a trailing dot.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(domainNamePattern, "must be a lowercase domain name without a trailing dot"),
				},
			},
			"run_search_and_replace": schema.BoolAttribute{
				MarkdownDescription: "Whether Kinsta rewrites the old primary URL to the new one in the WordPress " +
					"database when the primary domain changes. Defaults to `true`. Only used when a change is made.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
		},
	}
}

func (r *primaryDomainResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureData(req.ProviderData, &resp.Diagnostics)
}

func (r *primaryDomainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan primaryDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.apply(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *primaryDomainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan primaryDomainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.apply(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *primaryDomainResource) apply(ctx context.Context, plan *primaryDomainModel) (diags diag.Diagnostics) {
	ctx, cancel := context.WithTimeout(ctx, domainOperationTimeout)
	defer cancel()

	siteID, envID, name := plan.SiteID.ValueString(), plan.EnvironmentID.ValueString(), plan.DomainName.ValueString()
	current, err := r.primaryOf(ctx, siteID, envID)
	if err != nil {
		diags.AddError("Unable to read the environment", err.Error())
		return diags
	}
	if current != name {
		domain, err := r.data.client.FindSiteDomain(ctx, envID, name)
		if err != nil {
			diags.AddError("Unable to list site domains", err.Error())
			return diags
		}
		if domain == nil {
			diags.AddAttributeError(path.Root("domain_name"), "Not a domain of the environment",
				fmt.Sprintf("%s is not a domain of environment %s; add it with kinsta_site_domain first.", name, envID))
			return diags
		}
		if err := r.data.client.ChangePrimaryDomain(ctx, envID, domain.ID, plan.RunSearchAndReplace.ValueBool()); err != nil {
			diags.AddError("Unable to change the primary domain", err.Error())
			return diags
		}
		if current, err = r.primaryOf(ctx, siteID, envID); err != nil {
			diags.AddError("Unable to read the environment", err.Error())
			return diags
		}
		if current != name {
			diags.AddError("Primary domain did not change",
				fmt.Sprintf("Kinsta finished the change, but the primary domain of %s is %q, not %q.", envID, current, name))
			return diags
		}
	}
	plan.ID = types.StringValue(envID)
	return diags
}

func (r *primaryDomainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state primaryDomainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	current, err := r.primaryOf(ctx, state.SiteID.ValueString(), state.EnvironmentID.ValueString())
	if err != nil {
		if kinsta.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read the environment", err.Error())
		return
	}
	state.ID = state.EnvironmentID
	state.DomainName = types.StringValue(current)
	if state.RunSearchAndReplace.IsNull() {
		state.RunSearchAndReplace = types.BoolValue(true)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete leaves the primary domain in place: an environment always has one.
func (r *primaryDomainResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *primaryDomainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	siteID, envID, ok := strings.Cut(req.ID, "/")
	if !ok || siteID == "" || envID == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected `<site_id>/<environment_id>`, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("site_id"), siteID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("environment_id"), envID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), envID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("run_search_and_replace"), true)...)
}

// primaryOf returns the environment's primary domain name ("" when none). A
// missing site or environment is reported as a 404 APIError.
func (r *primaryDomainResource) primaryOf(ctx context.Context, siteID, envID string) (string, error) {
	site, err := r.data.client.GetSite(ctx, siteID)
	if err != nil {
		return "", err
	}
	env := findEnvironment(site.Environments, envID)
	if env == nil {
		return "", &kinsta.APIError{StatusCode: 404, Message: fmt.Sprintf("environment %s is not an environment of site %s", envID, siteID)}
	}
	if env.PrimaryDomain == nil {
		return "", nil
	}
	return kinsta.NormalizeName(env.PrimaryDomain.Name), nil
}
