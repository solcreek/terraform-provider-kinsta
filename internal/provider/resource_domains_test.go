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

func TestSiteDomainAndPrimaryDomain(t *testing.T) {
	f := newFakeKinsta(t)
	s := f.addSite("Blog", "")
	envID := s.Envs[0].ID

	config := func(primary string, extraDomain bool) string {
		deps := "kinsta_site_domain.apex"
		if extraDomain {
			deps += ", kinsta_site_domain.www"
		}
		cfg := f.providerConfig() + fmt.Sprintf(`
data "kinsta_site" "blog" {
  name = "blog"
}

resource "kinsta_site_domain" "apex" {
  environment_id = data.kinsta_site.blog.live_environment_id
  domain_name    = "example.com"
}

resource "kinsta_primary_domain" "blog" {
  site_id        = data.kinsta_site.blog.id
  environment_id = data.kinsta_site.blog.live_environment_id
  domain_name    = %q
  depends_on     = [%s]
}
`, primary, deps)
		if extraDomain {
			cfg += `
resource "kinsta_site_domain" "www" {
  environment_id = data.kinsta_site.blog.live_environment_id
  domain_name    = "www.example.com"
}
`
		}
		return cfg
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("example.com", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("kinsta_site_domain.apex", "id"),
					resource.TestCheckResourceAttr("kinsta_site_domain.apex", "verification_records.#", "1"),
					resource.TestCheckResourceAttr("kinsta_site_domain.apex", "verification_records.0.type", "TXT"),
					resource.TestCheckResourceAttr("kinsta_site_domain.apex", "pointing_records.0.value", "sites.kinsta.cloud"),
					resource.TestCheckResourceAttr("kinsta_primary_domain.blog", "id", envID),
					resource.TestCheckResourceAttr("kinsta_primary_domain.blog", "run_search_and_replace", "true"),
					func(*terraform.State) error {
						if got := f.primaryName(envID); got != "example.com" {
							return fmt.Errorf("primary domain = %q, want example.com", got)
						}
						return nil
					},
				),
			},
			{
				Config: config("example.com", false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName:      "kinsta_site_domain.apex",
				ImportState:       true,
				ImportStateId:     envID + "/example.com",
				ImportStateVerify: true,
			},
			{
				ResourceName:      "kinsta_primary_domain.blog",
				ImportState:       true,
				ImportStateId:     s.ID + "/" + envID,
				ImportStateVerify: true,
			},
			{
				// Moving the primary to a new domain in the same apply.
				Config: config("www.example.com", true),
				Check: func(*terraform.State) error {
					if got := f.primaryName(envID); got != "www.example.com" {
						return fmt.Errorf("primary domain = %q, want www.example.com", got)
					}
					return nil
				},
			},
			{
				// The primary changed in MyKinsta: drift is corrected.
				PreConfig: func() { f.setPrimary(envID, "blog.kinsta.cloud") },
				Config:    config("www.example.com", true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("kinsta_primary_domain.blog", plancheck.ResourceActionUpdate)},
				},
			},
			{
				// Before the managed domains can be destroyed, the primary must
				// move to a domain that stays (here the kinsta.cloud one).
				Config: config("blog.kinsta.cloud", true),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if f.hasDomain(envID, "example.com") {
				return fmt.Errorf("example.com still on the environment after destroy")
			}
			return nil
		},
	})
}

func TestSiteDomainCannotRemovePrimary(t *testing.T) {
	f := newFakeKinsta(t)
	s := f.addSite("Blog", "example.com")
	envID := s.Envs[0].ID
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: f.providerConfig() + fmt.Sprintf(`resource "kinsta_site_domain" "apex" {
  environment_id = %q
  domain_name    = "example.com"
}`, envID),
				ResourceName:       "kinsta_site_domain.apex",
				ImportState:        true,
				ImportStateId:      envID + "/example.com",
				ImportStatePersist: true,
			},
			{
				Config:      f.providerConfig(),
				ExpectError: regexp.MustCompile(`(?s)Site domain still present.*primary domain cannot be removed`),
			},
			{
				// Make another domain primary first; then it can go.
				PreConfig: func() { f.setPrimary(envID, "blog.kinsta.cloud") },
				Config:    f.providerConfig(),
			},
		},
	})
	if f.hasDomain(envID, "example.com") {
		t.Fatal("example.com still present")
	}
}

func TestSiteDomainValidation(t *testing.T) {
	f := newFakeKinsta(t)
	s := f.addSite("Blog", "example.com")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: f.providerConfig() + fmt.Sprintf(`resource "kinsta_site_domain" "d" {
  environment_id = %q
  domain_name    = "Example.COM."
}`, s.Envs[0].ID),
				ExpectError: regexp.MustCompile(`lowercase domain name`),
			},
			{
				Config: f.providerConfig() + fmt.Sprintf(`resource "kinsta_site_domain" "d" {
  environment_id = %q
  domain_name    = "example.com"
}`, s.Envs[0].ID),
				ExpectError: regexp.MustCompile(`(?s)already a domain.*Import it instead`),
			},
			{
				Config: f.providerConfig() + fmt.Sprintf(`
resource "kinsta_primary_domain" "p" {
  site_id        = %q
  environment_id = %q
  domain_name    = "nope.example.org"
}`, s.ID, s.Envs[0].ID),
				ExpectError: regexp.MustCompile(`Not a domain of the environment`),
			},
		},
	})
}

func (f *fakeKinsta) primaryName(envID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, e := f.env(envID)
	for _, d := range e.Domains {
		if d.ID == e.PrimaryDomainID {
			return d.Name
		}
	}
	return ""
}

func (f *fakeKinsta) setPrimary(envID, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, e := f.env(envID)
	for _, d := range e.Domains {
		if d.Name == name {
			e.PrimaryDomainID = d.ID
		}
	}
}

func (f *fakeKinsta) hasDomain(envID, name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, e := f.env(envID)
	for _, d := range e.Domains {
		if d.Name == name {
			return true
		}
	}
	return false
}
