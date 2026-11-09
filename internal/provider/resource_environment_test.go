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

func TestEnvironmentLifecycle(t *testing.T) {
	f := newFakeKinsta(t)
	s := f.addSite("Blog", "example.com")
	config := func(protect bool) string {
		return f.providerConfig() + fmt.Sprintf(`
resource "kinsta_environment" "staging" {
  site_id             = %q
  display_name        = "Staging"
  install_mode        = "plain"
  deletion_protection = %t
}
`, s.ID, protect)
	}
	const addr = "kinsta_environment.staging"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "id"),
					resource.TestCheckResourceAttr(addr, "name", "staging"),
					resource.TestCheckResourceAttr(addr, "is_premium", "false"),
					resource.TestCheckResourceAttr(addr, "php_version", "8.3"),
					resource.TestCheckResourceAttrSet(addr, "primary_domain"),
				),
			},
			{
				Config: config(true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName: addr,
				ImportState:  true,
				ImportStateIdFunc: func(st *terraform.State) (string, error) {
					return s.ID + "/" + st.RootModule().Resources[addr].Primary.ID, nil
				},
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"install_mode"},
			},
			{
				Config:      f.providerConfig(),
				ExpectError: regexp.MustCompile(`Environment is protected from deletion`),
			},
			{Config: config(false)},
		},
		CheckDestroy: func(*terraform.State) error {
			if n := len(f.site(s.ID).Envs); n != 1 {
				return fmt.Errorf("site has %d environments after destroy, want 1 (live)", n)
			}
			return nil
		},
	})
}

func TestEnvironmentRefusesLive(t *testing.T) {
	f := newFakeKinsta(t)
	s := f.addSite("Blog", "")
	live := s.Envs[0].ID
	cfg := f.providerConfig() + fmt.Sprintf(`
resource "kinsta_environment" "live" {
  site_id             = %q
  display_name        = "Live"
  is_premium          = true
  deletion_protection = false
}
`, s.ID)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             cfg,
				ResourceName:       "kinsta_environment.live",
				ImportState:        true,
				ImportStateId:      s.ID + "/" + live,
				ImportStatePersist: true,
			},
			{
				// deletion_protection imports as true; lift it, then try to delete.
				Config: cfg,
			},
			{
				Config:      f.providerConfig(),
				ExpectError: regexp.MustCompile(`Refusing to delete a live environment`),
			},
			{
				// Terraform 1.5 has no `removed` block to forget the resource, so
				// let the post-test destroy through by making the fake environment
				// a non-live one. The refusal above is what this test is about.
				PreConfig: func() {
					f.mu.Lock()
					f.sites[s.ID].Envs[0].Name = "retired"
					f.mu.Unlock()
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestDataSources(t *testing.T) {
	f := newFakeKinsta(t)
	s := f.addSite("Blog", "example.com")
	f.addSite("Shop", "")
	f.addZone("example.com")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: f.providerConfig() + fmt.Sprintf(`
data "kinsta_sites" "all" {}
data "kinsta_regions" "all" {}
data "kinsta_site" "by_name" {
  name = "BLOG"
}
data "kinsta_site" "by_id" {
  id = %q
}
data "kinsta_dns_zone" "z" {
  name = "Example.com."
}
data "kinsta_site_domain" "apex" {
  environment_id = data.kinsta_site.by_id.live_environment_id
  domain_name    = "example.com"
}
`, s.ID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.kinsta_sites.all", "sites.#", "2"),
					resource.TestCheckResourceAttr("data.kinsta_regions.all", "regions.#", "2"),
					resource.TestCheckTypeSetElemNestedAttrs("data.kinsta_regions.all", "regions.*", map[string]string{"region": "us-ashburn-1"}),
					resource.TestCheckResourceAttr("data.kinsta_site.by_name", "id", s.ID),
					resource.TestCheckResourceAttr("data.kinsta_site.by_name", "primary_domain", "example.com"),
					resource.TestCheckResourceAttrPair("data.kinsta_site.by_name", "live_environment_id", "data.kinsta_site.by_id", "live_environment_id"),
					resource.TestCheckResourceAttrSet("data.kinsta_dns_zone.z", "id"),
					resource.TestCheckResourceAttr("data.kinsta_dns_zone.z", "is_active", "true"),
					resource.TestCheckResourceAttr("data.kinsta_site_domain.apex", "pointing_records.#", "1"),
				),
			},
			{
				Config:      f.providerConfig() + `data "kinsta_dns_zone" "z" { name = "www.example.com" }`,
				ExpectError: regexp.MustCompile(`Did you mean\s+the\s+zone\s+example.com`),
			},
			{
				Config:      f.providerConfig() + `data "kinsta_site" "s" { name = "nope" }`,
				ExpectError: regexp.MustCompile(`Site not found`),
			},
			{
				Config: f.providerConfig() + `
data "kinsta_site" "s" {
  id   = "x"
  name = "y"
}`,
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
			{Config: f.providerConfig()},
		},
	})
}
