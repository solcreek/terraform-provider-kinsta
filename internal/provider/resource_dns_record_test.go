// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestDNSRecordLifecycle(t *testing.T) {
	f := newFakeKinsta(t)
	z := f.addZone("example.com")

	config := func(name, values string, ttl int) string {
		return f.providerConfig() + fmt.Sprintf(`
data "kinsta_dns_zone" "example" {
  name = "example.com"
}

resource "kinsta_dns_record" "spf" {
  zone_id = data.kinsta_dns_zone.example.id
  name    = %q
  type    = "TXT"
  values  = %s
  ttl     = %d
}
`, name, values, ttl)
	}
	const addr = "kinsta_dns_record.spf"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// A bare TXT value is stored quoted, but state keeps the bare spelling.
				Config: config("@", `["v=spf1 -all"]`, 3600),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "fqdn", "example.com"),
					resource.TestCheckResourceAttr(addr, "zone_name", "example.com"),
					resource.TestCheckResourceAttr(addr, "id", z.ID+"/TXT/example.com"),
					resource.TestCheckTypeSetElemAttr(addr, "values.*", "v=spf1 -all"),
					func(*terraform.State) error {
						got, ttl, ok := f.record(z.ID, "TXT", "example.com")
						if !ok || !slices.Equal(got, []string{`"v=spf1 -all"`}) || ttl != 3600 {
							return fmt.Errorf("stored record = %v ttl %d (found %v)", got, ttl, ok)
						}
						return nil
					},
				),
			},
			{
				// Re-planning the same configuration is a no-op.
				Config: config("@", `["v=spf1 -all"]`, 3600),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// The quoted spelling of the same value is equal, too.
				Config: config("example.com", `["\"v=spf1 -all\""]`, 3600),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
			},
			{
				// Values and TTL change in place, as a diff.
				Config: config("example.com", `["v=spf1 include:_spf.google.com -all", "google-site-verification=abc"]`, 600),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: func(*terraform.State) error {
					got, ttl, _ := f.record(z.ID, "TXT", "example.com")
					slices.Sort(got)
					want := []string{`"google-site-verification=abc"`, `"v=spf1 include:_spf.google.com -all"`}
					if !slices.Equal(got, want) || ttl != 600 {
						return fmt.Errorf("stored record = %v ttl %d, want %v ttl 600", got, ttl, want)
					}
					return nil
				},
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     z.ID + "/TXT/example.com",
				ImportStateVerify: true,
				// name is imported as the fqdn; the config says example.com too.
			},
			{
				// A record deleted outside Terraform is recreated.
				PreConfig: func() { f.deleteRecord(z.ID, "TXT", "example.com") },
				Config:    config("example.com", `["v=spf1 include:_spf.google.com -all", "google-site-verification=abc"]`, 600),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
				},
			},
			{
				// A value added outside Terraform is drift and is removed.
				PreConfig: func() {
					f.setRecord(z.ID, "TXT", "example.com", 600, `"v=spf1 include:_spf.google.com -all"`, `"google-site-verification=abc"`, `"rogue"`)
				},
				Config: config("example.com", `["v=spf1 include:_spf.google.com -all", "google-site-verification=abc"]`, 600),
				Check: func(*terraform.State) error {
					if got, _, _ := f.record(z.ID, "TXT", "example.com"); slices.Contains(got, `"rogue"`) {
						return fmt.Errorf("drifted value not removed: %v", got)
					}
					return nil
				},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			if _, _, ok := f.record(z.ID, "TXT", "example.com"); ok {
				return fmt.Errorf("record still exists after destroy")
			}
			return nil
		},
	})
}

func TestDNSRecordRelativeNameAndRespelling(t *testing.T) {
	f := newFakeKinsta(t)
	z := f.addZone("example.com")
	config := func(name string) string {
		return f.providerConfig() + fmt.Sprintf(`
resource "kinsta_dns_record" "www" {
  zone_id = %q
  name    = %q
  type    = "CNAME"
  values  = ["example.com"]
}
`, z.ID, name)
	}
	const addr = "kinsta_dns_record.www"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("www"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "fqdn", "www.example.com"),
					resource.TestCheckResourceAttr(addr, "ttl", "3600"),
				),
			},
			{
				// Another spelling of the same name is an in-place update, not a replacement.
				Config: config("www.example.com."),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
			},
			{
				// A different name replaces the record.
				Config: config("blog"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: func(*terraform.State) error {
					if _, _, ok := f.record(z.ID, "CNAME", "www.example.com"); ok {
						return fmt.Errorf("old record not removed")
					}
					if _, _, ok := f.record(z.ID, "CNAME", "blog.example.com"); !ok {
						return fmt.Errorf("new record not created")
					}
					return nil
				},
			},
		},
	})
}

func TestDNSRecordValidation(t *testing.T) {
	f := newFakeKinsta(t)
	z := f.addZone("example.com")
	f.setRecord(z.ID, "A", "existing.example.com", 3600, "192.0.2.1")
	rec := func(name, typ string, ttl int) string {
		return f.providerConfig() + fmt.Sprintf(`
resource "kinsta_dns_record" "r" {
  zone_id = %q
  name    = %q
  type    = %q
  values  = ["192.0.2.10"]
  ttl     = %d
}
`, z.ID, name, typ, ttl)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: rec("a", "a", 3600), ExpectError: regexp.MustCompile(`upper-case record type`)},
			{Config: rec("a", "A", 60), ExpectError: regexp.MustCompile(`must be at least 300`)},
			{Config: rec("host.other.org.", "A", 3600), ExpectError: regexp.MustCompile(`outside the zone`)},
			{Config: rec("existing", "A", 3600), ExpectError: regexp.MustCompile(`(?s)already exists.*Import it instead`)},
		},
	})
}

func TestDNSRecordZoneGone(t *testing.T) {
	f := newFakeKinsta(t)
	z := f.addZone("example.com")
	config := f.providerConfig() + fmt.Sprintf(`
resource "kinsta_dns_record" "a" {
  zone_id = %q
  name    = "@"
  type    = "A"
  values  = ["192.0.2.1"]
}
`, z.ID)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// The zone is removed: its records 500, and the record leaves state.
				PreConfig: func() {
					f.mu.Lock()
					delete(f.zones, z.ID)
					f.mu.Unlock()
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`Unknown zone`),
			},
		},
	})
}

// TestAccDNSRecord runs against the live Kinsta API. It creates and deletes
// a TXT record with a random name in KINSTA_TEST_ZONE, so it only runs when
// that variable names a zone the account may write to.
func TestAccDNSRecord(t *testing.T) {
	zone := os.Getenv("KINSTA_TEST_ZONE")
	if zone == "" {
		t.Skip("KINSTA_TEST_ZONE not set")
	}
	name := fmt.Sprintf("tf-acc-%s", randomSuffix())
	config := func(value string, ttl int) string {
		return requiredProviders + fmt.Sprintf(`
data "kinsta_dns_zone" "test" {
  name = %q
}

resource "kinsta_dns_record" "test" {
  zone_id = data.kinsta_dns_zone.test.id
  name    = %q
  type    = "TXT"
  values  = [%q]
  ttl     = %d
}
`, zone, name, value, ttl)
	}
	const addr = "kinsta_dns_record.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("terraform-provider-kinsta acceptance test", 300),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "fqdn", name+"."+zone),
					resource.TestCheckTypeSetElemAttr(addr, "values.*", "terraform-provider-kinsta acceptance test"),
				),
			},
			{
				Config: config("terraform-provider-kinsta acceptance test, updated", 600),
				Check:  resource.TestCheckResourceAttr(addr, "ttl", "600"),
			},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateIdFunc:       func(s *terraform.State) (string, error) { return s.RootModule().Resources[addr].Primary.ID, nil },
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"name"},
			},
		},
	})
}
