// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Install modes shared by kinsta_site and kinsta_environment.
const (
	installNew   = "new"
	installPlain = "plain"
)

// installAttributes are the WordPress installation settings sent when a site
// or environment is created. Kinsta never returns them, so they are
// creation-only: changing one later records the new value without touching
// the installation (and without replacing the site).
var installAttributes = []string{
	"install_mode", "admin_email", "admin_user", "admin_password", "site_title",
	"wp_language", "is_multisite", "is_subdomain_multisite", "woocommerce", "wordpressseo",
}

// requiredForNewInstall must be set when install_mode is "new".
var requiredForNewInstall = []string{"admin_email", "admin_user", "admin_password", "site_title"}

func installSchemaAttributes(what string) map[string]schema.Attribute {
	creationOnly := " Only used when the " + what + " is created; see *Creation-only arguments*."
	return map[string]schema.Attribute{
		"install_mode": schema.StringAttribute{
			MarkdownDescription: "`new` (default) installs WordPress with the admin user below. `plain` creates an empty " +
				what + " without WordPress, for content deployed by other means; the admin and WordPress " +
				"arguments are then ignored." + creationOnly,
			Optional:   true,
			Validators: []validator.String{stringvalidator.OneOf(installNew, installPlain)},
		},
		"admin_email": schema.StringAttribute{
			MarkdownDescription: "WordPress admin email. Required for `install_mode = \"new\"`." + creationOnly,
			Optional:            true,
		},
		"admin_user": schema.StringAttribute{
			MarkdownDescription: "WordPress admin username. Required for `install_mode = \"new\"`." + creationOnly,
			Optional:            true,
		},
		"admin_password": schema.StringAttribute{
			MarkdownDescription: "WordPress admin password. Required for `install_mode = \"new\"`. Stored in state " +
				"(marked sensitive); change it in WordPress afterwards if state is shared." + creationOnly,
			Optional:  true,
			Sensitive: true,
		},
		"site_title": schema.StringAttribute{
			MarkdownDescription: "WordPress site title. Required for `install_mode = \"new\"`." + creationOnly,
			Optional:            true,
		},
		"wp_language": schema.StringAttribute{
			MarkdownDescription: "WordPress locale, e.g. `en_US` (the default when unset)." + creationOnly,
			Optional:            true,
		},
		"is_multisite": schema.BoolAttribute{
			MarkdownDescription: "Install WordPress Multisite." + creationOnly,
			Optional:            true,
		},
		"is_subdomain_multisite": schema.BoolAttribute{
			MarkdownDescription: "Use subdomains rather than subdirectories for Multisite." + creationOnly,
			Optional:            true,
		},
		"woocommerce": schema.BoolAttribute{
			MarkdownDescription: "Install WooCommerce." + creationOnly,
			Optional:            true,
		},
		"wordpressseo": schema.BoolAttribute{
			MarkdownDescription: "Install Yoast SEO." + creationOnly,
			Optional:            true,
		},
	}
}

// installMode returns the effective install mode (default "new").
func installMode(v types.String) string {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return installNew
	}
	return v.ValueString()
}

// planInstallDiagnostics checks the installation arguments at plan time:
// on create, "new" needs the admin user; on update, a changed creation-only
// argument gets a warning that it will not be applied.
func planInstallDiagnostics(ctx context.Context, plan tfsdk.Plan, state tfsdk.State, what string) (diags diag.Diagnostics) {
	if plan.Raw.IsNull() {
		return nil // destroy
	}
	if state.Raw.IsNull() {
		var mode types.String
		diags.Append(plan.GetAttribute(ctx, path.Root("install_mode"), &mode)...)
		if mode.IsUnknown() || installMode(mode) != installNew {
			return diags
		}
		for _, name := range requiredForNewInstall {
			var v types.String
			diags.Append(plan.GetAttribute(ctx, path.Root(name), &v)...)
			if v.IsNull() || (!v.IsUnknown() && v.ValueString() == "") {
				diags.AddAttributeError(path.Root(name), "Missing WordPress installation argument",
					fmt.Sprintf("`%s` is required to create this %s with install_mode = \"new\" (the default). "+
						"Set it, or use install_mode = \"plain\" for an empty %s. If this plan replaces an "+
						"existing %s, check which change forces the replacement.", name, what, what, what))
			}
		}
		return diags
	}
	var changed []string
	for _, name := range installAttributes {
		var before, after attr.Value
		if strings.HasPrefix(name, "is_") || name == "woocommerce" || name == "wordpressseo" {
			var b, a types.Bool
			diags.Append(state.GetAttribute(ctx, path.Root(name), &b)...)
			diags.Append(plan.GetAttribute(ctx, path.Root(name), &a)...)
			before, after = b, a
		} else {
			var b, a types.String
			diags.Append(state.GetAttribute(ctx, path.Root(name), &b)...)
			diags.Append(plan.GetAttribute(ctx, path.Root(name), &a)...)
			before, after = b, a
		}
		if !before.IsNull() && !before.Equal(after) {
			changed = append(changed, "`"+name+"`")
		}
	}
	if len(changed) > 0 {
		diags.AddWarning("Creation-only arguments changed",
			fmt.Sprintf("%s only apply when the %s is created. The new value is recorded in state, "+
				"but the existing WordPress installation is not changed.", strings.Join(changed, ", "), what))
	}
	return diags
}

// pollUntil calls check every interval until it reports done, the context
// ends, or timeout passes.
func pollUntil(ctx context.Context, timeout time.Duration, check func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		done, err := check()
		if err != nil || done {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("still not done after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval()):
		}
	}
}
