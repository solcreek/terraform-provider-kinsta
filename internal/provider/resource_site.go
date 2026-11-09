// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/solcreek/terraform-provider-kinsta/internal/kinsta"
)

var (
	_ resource.Resource                = (*siteResource)(nil)
	_ resource.ResourceWithConfigure   = (*siteResource)(nil)
	_ resource.ResourceWithImportState = (*siteResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*siteResource)(nil)
)

type siteResource struct {
	data *providerData
}

func newSiteResource() resource.Resource { return &siteResource{} }

type siteModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	DisplayName        types.String `tfsdk:"display_name"`
	Region             types.String `tfsdk:"region"`
	Status             types.String `tfsdk:"status"`
	CompanyID          types.String `tfsdk:"company_id"`
	LiveEnvironmentID  types.String `tfsdk:"live_environment_id"`
	PrimaryDomain      types.String `tfsdk:"primary_domain"`
	Environments       types.List   `tfsdk:"environments"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`

	InstallMode          types.String `tfsdk:"install_mode"`
	AdminEmail           types.String `tfsdk:"admin_email"`
	AdminUser            types.String `tfsdk:"admin_user"`
	AdminPassword        types.String `tfsdk:"admin_password"`
	SiteTitle            types.String `tfsdk:"site_title"`
	WPLanguage           types.String `tfsdk:"wp_language"`
	IsMultisite          types.Bool   `tfsdk:"is_multisite"`
	IsSubdomainMultisite types.Bool   `tfsdk:"is_subdomain_multisite"`
	WooCommerce          types.Bool   `tfsdk:"woocommerce"`
	WordPressSEO         types.Bool   `tfsdk:"wordpressseo"`
}

var environmentSummaryAttrTypes = map[string]attr.Type{
	"id":           types.StringType,
	"name":         types.StringType,
	"display_name": types.StringType,
	"is_premium":   types.BoolType,
}

func (r *siteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_site"
}

func (r *siteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computedString := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{
			MarkdownDescription: desc,
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		}
	}
	attrs := map[string]schema.Attribute{
		"id":   computedString("Site ID."),
		"name": computedString("Site name Kinsta derives from the display name (used in hostnames such as `<name>.kinsta.cloud`)."),
		"display_name": schema.StringAttribute{
			MarkdownDescription: "Name shown in MyKinsta. Kinsta has no API to rename a site, so changing it " +
				"replaces the site — which `deletion_protection` blocks by default.",
			Required:      true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"region": schema.StringAttribute{
			MarkdownDescription: "Data center region, e.g. `us-ashburn-1`; list them with the `kinsta_regions` data source. " +
				"Required to create a site. Kinsta does not report it back, so it is creation-only.",
			Optional: true,
		},
		"status":              computedString("Site status, e.g. `live`."),
		"company_id":          computedString("Company the site belongs to."),
		"live_environment_id": computedString("ID of the site's live environment."),
		"primary_domain": schema.StringAttribute{
			MarkdownDescription: "Primary domain of the live environment.",
			Computed:            true,
		},
		"environments": schema.ListNestedAttribute{
			MarkdownDescription: "The site's environments.",
			Computed:            true,
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"id":           schema.StringAttribute{Computed: true, MarkdownDescription: "Environment ID."},
				"name":         schema.StringAttribute{Computed: true, MarkdownDescription: "Environment name, e.g. `live` or `staging`."},
				"display_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Environment display name."},
				"is_premium":   schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether it is a premium environment."},
			}},
		},
		"deletion_protection": schema.BoolAttribute{
			MarkdownDescription: "Refuse to delete the site. Defaults to `true`: deleting a site removes every " +
				"environment, its database and its files, and Kinsta has no undo. Set it to `false` and apply " +
				"before destroying the site.",
			Optional: true,
			Computed: true,
			Default:  booldefault.StaticBool(true),
		},
	}
	for k, v := range installSchemaAttributes("site") {
		attrs[k] = v
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a WordPress site on Kinsta, created with a live environment.",
		Attributes:          attrs,
	}
}

func (r *siteResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureData(req.ProviderData, &resp.Diagnostics)
}

func (r *siteResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	resp.Diagnostics.Append(planInstallDiagnostics(ctx, req.Plan, req.State, "site")...)
	if req.State.Raw.IsNull() && !req.Plan.Raw.IsNull() {
		var region types.String
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("region"), &region)...)
		if region.IsNull() || (!region.IsUnknown() && region.ValueString() == "") {
			resp.Diagnostics.AddAttributeError(path.Root("region"), "Missing region",
				"`region` is required to create a site. List the available regions with the kinsta_regions data source.")
		}
	}
}

func (r *siteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan siteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, siteOperationTimeout)
	defer cancel()

	displayName := plan.DisplayName.ValueString()
	// The new site may have to be found by display name afterwards, so the
	// name must be unique, and a retried apply must not start a second site.
	if taken, err := r.findByDisplayName(ctx, displayName); err != nil {
		resp.Diagnostics.AddError("Unable to list sites", err.Error())
		return
	} else if taken != nil {
		resp.Diagnostics.AddAttributeError(path.Root("display_name"), "Site already exists",
			fmt.Sprintf("A site named %q already exists (%s, id %s). Import it instead: terraform import <address> %s",
				displayName, taken.Name, taken.ID, taken.ID))
		return
	}

	var (
		op  *kinsta.Operation
		err error
	)
	if installMode(plan.InstallMode) == installPlain {
		op, err = r.data.client.CreatePlainSite(ctx, kinsta.CreatePlainSiteRequest{
			Company: r.data.companyID, DisplayName: displayName, Region: plan.Region.ValueString(),
		})
	} else {
		op, err = r.data.client.CreateSite(ctx, kinsta.CreateSiteRequest{
			Company:              r.data.companyID,
			DisplayName:          displayName,
			Region:               plan.Region.ValueString(),
			InstallMode:          installNew,
			AdminEmail:           plan.AdminEmail.ValueString(),
			AdminUser:            plan.AdminUser.ValueString(),
			AdminPassword:        plan.AdminPassword.ValueString(),
			SiteTitle:            plan.SiteTitle.ValueString(),
			WPLanguage:           valueOr(plan.WPLanguage, "en_US"),
			IsMultisite:          plan.IsMultisite.ValueBool(),
			IsSubdomainMultisite: plan.IsSubdomainMultisite.ValueBool(),
			WooCommerce:          plan.WooCommerce.ValueBool(),
			WordPressSEO:         plan.WordPressSEO.ValueBool(),
		})
	}

	siteID := op.DataString("idSite")
	if siteID == "" {
		// Not every operation reports the new id: find the site by name. This
		// also catches a site whose creation outlived the poll window, so it
		// is saved (tainted) instead of being orphaned.
		if site, ferr := r.findByDisplayName(context.WithoutCancel(ctx), displayName); ferr == nil && site != nil {
			siteID = site.ID
		}
	}
	if siteID != "" {
		plan.ID = types.StringValue(siteID)
		resp.Diagnostics.Append(r.read(context.WithoutCancel(ctx), &plan)...)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to create site", err.Error())
		return
	}
	if siteID == "" {
		resp.Diagnostics.AddError("Site not found after creation",
			fmt.Sprintf("Kinsta finished creating %q but no site with that display name is listed. Check MyKinsta.", displayName))
	}
}

func (r *siteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state siteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.data.client.GetSite(ctx, state.ID.ValueString()); kinsta.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(r.read(ctx, &state)...)
	if state.DeletionProtection.IsNull() {
		state.DeletionProtection = types.BoolValue(true)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update only records new values: deletion_protection and the creation-only
// arguments have no API counterpart.
func (r *siteResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan siteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.read(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *siteResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state siteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.DeletionProtection.IsNull() || state.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Site is protected from deletion",
			fmt.Sprintf("kinsta_site %q (%s) has deletion_protection = true. Deleting a site removes every environment, "+
				"its database and its files, and cannot be undone. Back the site up, set deletion_protection = false, "+
				"apply, and then destroy.", state.DisplayName.ValueString(), state.ID.ValueString()))
		return
	}
	ctx, cancel := context.WithTimeout(ctx, siteOperationTimeout)
	defer cancel()

	siteID := state.ID.ValueString()
	if err := r.data.client.DeleteSite(ctx, siteID); err != nil && !kinsta.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to delete site", err.Error())
		return
	}
	// The site being gone decides success, not the operation status: a
	// failed delete can still report a finished operation.
	err := pollUntil(ctx, siteOperationTimeout, func() (bool, error) {
		_, err := r.data.client.GetSite(ctx, siteID)
		if kinsta.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		resp.Diagnostics.AddError("Site still exists",
			fmt.Sprintf("Kinsta accepted the delete of site %s, but it is still listed: %s. Check MyKinsta.", siteID, err))
	}
}

func (r *siteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), true)...)
}

// read fills the computed attributes from the API.
func (r *siteResource) read(ctx context.Context, m *siteModel) (diags diag.Diagnostics) {
	site, err := r.data.client.GetSite(ctx, m.ID.ValueString())
	if err != nil {
		diags.AddError("Unable to read site", err.Error())
		return diags
	}
	m.Name = types.StringValue(site.Name)
	// Keep the configured spelling when only the case differs, so a
	// normalised name never plans a replacement.
	if !strings.EqualFold(m.DisplayName.ValueString(), site.DisplayName) {
		m.DisplayName = types.StringValue(site.DisplayName)
	}
	m.Status = types.StringValue(site.Status)
	m.CompanyID = types.StringValue(site.CompanyID)
	m.LiveEnvironmentID = types.StringNull()
	m.PrimaryDomain = types.StringNull()
	if live := site.LiveEnvironment(); live != nil {
		m.LiveEnvironmentID = types.StringValue(live.ID)
		if live.PrimaryDomain != nil {
			m.PrimaryDomain = types.StringValue(kinsta.NormalizeName(live.PrimaryDomain.Name))
		}
	}
	envs, d := environmentSummaries(site.Environments)
	diags.Append(d...)
	m.Environments = envs
	return diags
}

func environmentSummaries(envs []kinsta.Environment) (types.List, diag.Diagnostics) {
	objType := types.ObjectType{AttrTypes: environmentSummaryAttrTypes}
	var diags diag.Diagnostics
	elems := make([]attr.Value, 0, len(envs))
	for _, e := range envs {
		obj, d := types.ObjectValue(environmentSummaryAttrTypes, map[string]attr.Value{
			"id":           types.StringValue(e.ID),
			"name":         types.StringValue(e.Name),
			"display_name": types.StringValue(e.DisplayName),
			"is_premium":   types.BoolValue(e.IsPremium),
		})
		diags.Append(d...)
		elems = append(elems, obj)
	}
	list, d := types.ListValue(objType, elems)
	diags.Append(d...)
	return list, diags
}

func (r *siteResource) findByDisplayName(ctx context.Context, displayName string) (*kinsta.Site, error) {
	sites, err := r.data.client.ListSites(ctx, r.data.companyID)
	if err != nil {
		return nil, err
	}
	for i := range sites {
		if strings.EqualFold(sites[i].DisplayName, displayName) {
			return &sites[i], nil
		}
	}
	return nil, nil
}

func valueOr(v types.String, fallback string) string {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return fallback
	}
	return v.ValueString()
}
