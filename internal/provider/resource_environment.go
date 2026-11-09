// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/solcreek/terraform-provider-kinsta/internal/kinsta"
)

var (
	_ resource.Resource                = (*environmentResource)(nil)
	_ resource.ResourceWithConfigure   = (*environmentResource)(nil)
	_ resource.ResourceWithImportState = (*environmentResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*environmentResource)(nil)
)

type environmentResource struct {
	data *providerData
}

func newEnvironmentResource() resource.Resource { return &environmentResource{} }

type environmentModel struct {
	ID                 types.String `tfsdk:"id"`
	SiteID             types.String `tfsdk:"site_id"`
	Name               types.String `tfsdk:"name"`
	DisplayName        types.String `tfsdk:"display_name"`
	IsPremium          types.Bool   `tfsdk:"is_premium"`
	PrimaryDomain      types.String `tfsdk:"primary_domain"`
	PHPVersion         types.String `tfsdk:"php_version"`
	WebRoot            types.String `tfsdk:"web_root"`
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

func (r *environmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment"
}

func (r *environmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			MarkdownDescription: "Environment ID.",
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"site_id": schema.StringAttribute{
			MarkdownDescription: "ID of the site to add the environment to.",
			Required:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"name": schema.StringAttribute{
			MarkdownDescription: "Environment name Kinsta derives from the display name.",
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"display_name": schema.StringAttribute{
			MarkdownDescription: "Environment display name, e.g. `Staging`. Changing it replaces the environment.",
			Required:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"is_premium": schema.BoolAttribute{
			MarkdownDescription: "Create a premium staging environment (billed separately). Defaults to `false`. " +
				"Changing it replaces the environment.",
			Optional:      true,
			Computed:      true,
			Default:       booldefault.StaticBool(false),
			PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
		},
		"primary_domain": schema.StringAttribute{
			MarkdownDescription: "Primary domain of the environment.",
			Computed:            true,
		},
		"php_version": schema.StringAttribute{
			MarkdownDescription: "PHP version the environment runs, e.g. `8.3`.",
			Computed:            true,
		},
		"web_root": schema.StringAttribute{
			MarkdownDescription: "Path of the environment's web root on the server.",
			Computed:            true,
		},
		"deletion_protection": schema.BoolAttribute{
			MarkdownDescription: "Refuse to delete the environment. Defaults to `true`, because deleting an " +
				"environment removes its files and database. Set it to `false` and apply before destroying it.",
			Optional: true,
			Computed: true,
			Default:  booldefault.StaticBool(true),
		},
	}
	for k, v := range installSchemaAttributes("environment") {
		attrs[k] = v
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a staging environment of a WordPress site. A site's live environment is " +
			"created with the site and cannot be managed (or deleted) through this resource.",
		Attributes: attrs,
	}
}

func (r *environmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureData(req.ProviderData, &resp.Diagnostics)
}

func (r *environmentResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	resp.Diagnostics.Append(planInstallDiagnostics(ctx, req.Plan, req.State, "environment")...)
}

func (r *environmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, siteOperationTimeout)
	defer cancel()

	siteID, displayName := plan.SiteID.ValueString(), plan.DisplayName.ValueString()
	before, err := r.data.client.ListEnvironments(ctx, siteID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list the site's environments", err.Error())
		return
	}
	known := map[string]bool{}
	for _, e := range before {
		known[e.ID] = true
		if strings.EqualFold(e.DisplayName, displayName) {
			resp.Diagnostics.AddAttributeError(path.Root("display_name"), "Environment already exists",
				fmt.Sprintf("Site %s already has an environment named %q (id %s). Import it instead: terraform import <address> %s/%s",
					siteID, e.DisplayName, e.ID, siteID, e.ID))
			return
		}
	}

	var op *kinsta.Operation
	if installMode(plan.InstallMode) == installPlain {
		op, err = r.data.client.CreatePlainEnvironment(ctx, siteID, kinsta.CreatePlainEnvironmentRequest{
			DisplayName: displayName, IsPremium: plan.IsPremium.ValueBool(),
		})
	} else {
		op, err = r.data.client.CreateEnvironment(ctx, siteID, kinsta.CreateEnvironmentRequest{
			DisplayName:          displayName,
			IsPremium:            plan.IsPremium.ValueBool(),
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

	envID := op.DataString("idEnv")
	if envID == "" {
		// Find the environment that was not there before, by display name.
		if after, lerr := r.data.client.ListEnvironments(context.WithoutCancel(ctx), siteID); lerr == nil {
			for _, e := range after {
				if !known[e.ID] && strings.EqualFold(e.DisplayName, displayName) {
					envID = e.ID
				}
			}
		}
	}
	if envID != "" {
		plan.ID = types.StringValue(envID)
		if _, d := r.read(context.WithoutCancel(ctx), &plan); !d.HasError() {
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		} else {
			resp.Diagnostics.Append(d...)
		}
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to create environment", err.Error())
		return
	}
	if envID == "" {
		resp.Diagnostics.AddError("Environment not found after creation",
			fmt.Sprintf("Kinsta finished creating %q on site %s but it is not listed. Check MyKinsta.", displayName, siteID))
	}
}

func (r *environmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, d := r.read(ctx, &state)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	if state.DeletionProtection.IsNull() {
		state.DeletionProtection = types.BoolValue(true)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update only records new values: deletion_protection and the creation-only
// arguments have no API counterpart.
func (r *environmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, d := r.read(ctx, &plan)
	resp.Diagnostics.Append(d...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *environmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Name.ValueString() == "live" {
		resp.Diagnostics.AddError("Refusing to delete a live environment",
			"The live environment is deleted only together with its site (kinsta_site).")
		return
	}
	if state.DeletionProtection.IsNull() || state.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Environment is protected from deletion",
			fmt.Sprintf("kinsta_environment %q (%s) has deletion_protection = true. Deleting it removes its files and "+
				"database. Set deletion_protection = false, apply, and then destroy.", state.DisplayName.ValueString(), state.ID.ValueString()))
		return
	}
	ctx, cancel := context.WithTimeout(ctx, siteOperationTimeout)
	defer cancel()

	siteID, envID := state.SiteID.ValueString(), state.ID.ValueString()
	if err := r.data.client.DeleteEnvironment(ctx, envID); err != nil && !kinsta.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to delete environment", err.Error())
		return
	}
	err := pollUntil(ctx, siteOperationTimeout, func() (bool, error) {
		envs, err := r.data.client.ListEnvironments(ctx, siteID)
		if kinsta.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return findEnvironment(envs, envID) == nil, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Environment still exists",
			fmt.Sprintf("Kinsta accepted the delete of environment %s, but it is still listed: %s. Check MyKinsta.", envID, err))
	}
}

func (r *environmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	siteID, envID, ok := strings.Cut(req.ID, "/")
	if !ok || siteID == "" || envID == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected `<site_id>/<environment_id>`, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("site_id"), siteID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), envID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), true)...)
}

// read fills the computed attributes. It reports false when the site or the
// environment no longer exists.
func (r *environmentResource) read(ctx context.Context, m *environmentModel) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	envs, err := r.data.client.ListEnvironments(ctx, m.SiteID.ValueString())
	if err != nil {
		if kinsta.IsNotFound(err) {
			return false, diags
		}
		diags.AddError("Unable to read the site's environments", err.Error())
		return false, diags
	}
	env := findEnvironment(envs, m.ID.ValueString())
	if env == nil {
		return false, diags
	}
	m.Name = types.StringValue(env.Name)
	// Keep the configured spelling when only the case differs, so a
	// normalised name never plans a replacement.
	if !strings.EqualFold(m.DisplayName.ValueString(), env.DisplayName) {
		m.DisplayName = types.StringValue(env.DisplayName)
	}
	m.IsPremium = types.BoolValue(env.IsPremium)
	m.WebRoot = types.StringValue(env.WebRoot)
	m.PHPVersion = types.StringValue(env.PHPVersion())
	m.PrimaryDomain = types.StringNull()
	if env.PrimaryDomain != nil {
		m.PrimaryDomain = types.StringValue(kinsta.NormalizeName(env.PrimaryDomain.Name))
	}
	return true, diags
}
