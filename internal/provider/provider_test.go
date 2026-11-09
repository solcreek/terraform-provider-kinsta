// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestMain serves the in-process provider under the solcreek namespace, which
// the test configurations name in required_providers, so the same tests run
// under Terraform and OpenTofu (TF_ACC_TERRAFORM_PATH=$(command -v tofu)).
func TestMain(m *testing.M) {
	if os.Getenv("TF_ACC_PROVIDER_NAMESPACE") == "" {
		_ = os.Setenv("TF_ACC_PROVIDER_NAMESPACE", "solcreek")
	}
	if os.Getenv("TF_ACC_PROVIDER_HOST") == "" && filepath.Base(os.Getenv("TF_ACC_TERRAFORM_PATH")) == "tofu" {
		_ = os.Setenv("TF_ACC_PROVIDER_HOST", "registry.opentofu.org")
	}
	os.Exit(m.Run())
}

// requiredProviders pins the provider source in every test configuration.
const requiredProviders = `
terraform {
  required_providers {
    kinsta = {
      source = "solcreek/kinsta"
    }
  }
}
`

// testProtoV6ProviderFactories wires the in-process provider for tests.
var testProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"kinsta": providerserver.NewProtocol6WithError(New("test")()),
}

// testAccPreCheck verifies the environment for acceptance tests, which make
// real API calls and require:
//   - TF_ACC=1
//   - KINSTA_API_KEY  a Kinsta API key (KINSTA_COMPANY_ID is optional)
func testAccPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("KINSTA_API_KEY") == "" {
		t.Fatal("KINSTA_API_KEY must be set for acceptance tests")
	}
}

func TestProviderMissingCredentials(t *testing.T) {
	newFakeKinsta(t) // clears KINSTA_* from the environment
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      requiredProviders + `data "kinsta_regions" "all" {}`,
			ExpectError: regexp.MustCompile(`Missing Kinsta credentials`),
		}},
	})
}

func TestProviderInvalidKey(t *testing.T) {
	f := newFakeKinsta(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: requiredProviders + `
provider "kinsta" {
  api_key = "wrong"
  api_url = "` + f.srv.URL + `"
}
data "kinsta_regions" "all" {}
`,
			ExpectError: regexp.MustCompile(`(?s)Unable to look up the company.*invalid,\s+expired\s+or\s+revoked`),
		}},
	})
}

func TestProviderExplicitCompanySkipsValidate(t *testing.T) {
	f := newFakeKinsta(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: requiredProviders + `
provider "kinsta" {
  api_key    = "test"
  api_url    = "` + f.srv.URL + `"
  company_id = "` + fakeCompany + `"
}
data "kinsta_regions" "all" {}
`,
			Check: func(*terraform.State) error {
				f.mu.Lock()
				defer f.mu.Unlock()
				for _, r := range f.requests {
					if r == "GET /validate" {
						return fmt.Errorf("company_id was set, but the provider still called /validate")
					}
				}
				return nil
			},
		}},
	})
}

// randomSuffix returns a short random hex string for acceptance test names.
func randomSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
