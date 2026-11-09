// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestSiteLifecycle(t *testing.T) {
	f := newFakeKinsta(t)
	site := func(protect bool, extra string) string {
		return f.providerConfig() + fmt.Sprintf(`
resource "kinsta_site" "blog" {
  display_name        = "Blog"
  region              = "us-ashburn-1"
  admin_email         = "admin@example.com"
  admin_user          = "admin"
  admin_password      = "correct horse battery staple"
  site_title          = "My Blog"
  deletion_protection = %t
  %s
}
`, protect, extra)
	}
	const addr = "kinsta_site.blog"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: site(true, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "id"),
					resource.TestCheckResourceAttr(addr, "name", "blog"),
					resource.TestCheckResourceAttr(addr, "status", "live"),
					resource.TestCheckResourceAttr(addr, "company_id", fakeCompany),
					resource.TestCheckResourceAttr(addr, "primary_domain", "blog.kinsta.cloud"),
					resource.TestCheckResourceAttr(addr, "environments.#", "1"),
					resource.TestCheckResourceAttr(addr, "environments.0.name", "live"),
					resource.TestCheckResourceAttrPair(addr, "live_environment_id", addr, "environments.0.id"),
				),
			},
			{
				Config: site(true, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Creation-only arguments change in place, never replace the site.
				Config: site(true, `woocommerce = true`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"region", "admin_email", "admin_user", "admin_password", "site_title", "woocommerce",
				},
			},
			{
				// Removing the protected site from the configuration fails.
				Config:      f.providerConfig(),
				ExpectError: regexp.MustCompile(`Site is protected from deletion`),
			},
			{
				Config: site(false, ""),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if s := f.siteByDisplayName("Blog"); s != nil {
				return fmt.Errorf("site %s still exists after destroy", s.ID)
			}
			return nil
		},
	})
}

func TestSiteRenameBlockedByProtection(t *testing.T) {
	f := newFakeKinsta(t)
	existing := f.addSite("Shop", "shop.example.com")
	config := func(name string, protect bool) string {
		return f.providerConfig() + fmt.Sprintf(`
resource "kinsta_site" "shop" {
  display_name        = %q
  deletion_protection = %t
}
`, name, protect)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// An existing site is adopted by import; no region or admin needed.
				Config:             config("Shop", true),
				ResourceName:       "kinsta_site.shop",
				ImportState:        true,
				ImportStateId:      existing.ID,
				ImportStatePersist: true,
			},
			{
				Config: config("Shop", true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr("kinsta_site.shop", "primary_domain", "shop.example.com"),
			},
			{
				// A rename means a new site: the plan stops on the missing
				// installation arguments, and an apply would stop on protection.
				Config:      config("Shop Renamed", true),
				ExpectError: regexp.MustCompile(`Missing WordPress installation argument`),
			},
			{
				Config: config("Shop", false),
			},
		},
	})
	if f.site(existing.ID) != nil {
		t.Fatalf("site was not destroyed after protection was lifted")
	}
}

func TestSiteValidation(t *testing.T) {
	f := newFakeKinsta(t)
	f.addSite("Taken", "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: f.providerConfig() + `
resource "kinsta_site" "s" {
  display_name = "New"
  region       = "us-ashburn-1"
}
`,
				ExpectError: regexp.MustCompile(`admin_email.*is required`),
			},
			{
				Config: f.providerConfig() + `
resource "kinsta_site" "s" {
  display_name = "New"
  install_mode = "plain"
}
`,
				ExpectError: regexp.MustCompile(`Missing region`),
			},
			{
				Config: f.providerConfig() + `
resource "kinsta_site" "s" {
  display_name = "Taken"
  region       = "us-ashburn-1"
  install_mode = "plain"
}
`,
				ExpectError: regexp.MustCompile(`(?s)Site already exists.*Import it instead`),
			},
		},
	})
}

func TestSitePlainInstall(t *testing.T) {
	f := newFakeKinsta(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: f.providerConfig() + `
resource "kinsta_site" "empty" {
  display_name        = "Empty"
  region              = "ap-tokyo-1"
  install_mode        = "plain"
  deletion_protection = false
}
`,
				Check: resource.TestCheckResourceAttr("kinsta_site.empty", "name", "empty"),
			},
		},
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	var plain bool
	for _, r := range f.requests {
		if r == "POST /sites/plain" {
			plain = true
		}
		if r == "POST /sites" {
			t.Errorf("install_mode = plain must not use POST /sites")
		}
	}
	if !plain {
		t.Errorf("install_mode = plain did not call POST /sites/plain")
	}
}

func TestSiteCreateFailureSurfacesReason(t *testing.T) {
	f := newFakeKinsta(t)
	f.failRequests("POST")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: f.providerConfig() + `
resource "kinsta_site" "s" {
  display_name = "Broken"
  region       = "us-ashburn-1"
  install_mode = "plain"
}
`,
			ExpectError: regexp.MustCompile(`(?s)Unable to create site.*injected failure`),
		}},
	})
}
