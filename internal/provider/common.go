// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/solcreek/terraform-provider-kinsta/internal/kinsta"
)

// Upper bounds for the operations Kinsta runs asynchronously. Terraform's
// own context still applies; these only stop a stuck operation from blocking
// an apply forever.
const (
	siteOperationTimeout   = 60 * time.Minute
	domainOperationTimeout = 20 * time.Minute
	dnsOperationTimeout    = 10 * time.Minute
)

// qualifyName turns a record name into the fully qualified, lowercase name
// (without trailing dot) the API matches on: `@` or "" is the zone apex, a
// trailing dot marks a name as absolute, a name already inside the zone is
// kept, and anything else is relative to the zone.
func qualifyName(name, zone string) string {
	raw := strings.ToLower(strings.TrimSpace(name))
	apex := kinsta.NormalizeName(zone)
	switch {
	case raw == "@" || raw == "":
		return apex
	case strings.HasSuffix(raw, "."):
		return strings.TrimSuffix(raw, ".")
	case raw == apex || strings.HasSuffix(raw, "."+apex):
		return raw
	default:
		return raw + "." + apex
	}
}

// inZone reports whether fqdn lies inside zone.
func inZone(fqdn, zone string) bool {
	apex := kinsta.NormalizeName(zone)
	return fqdn == apex || strings.HasSuffix(fqdn, "."+apex)
}

// recordHintAttrTypes describes a DNS record Kinsta asks for.
var recordHintAttrTypes = map[string]attr.Type{
	"type":  types.StringType,
	"name":  types.StringType,
	"value": types.StringType,
}

var recordHintType = types.ObjectType{AttrTypes: recordHintAttrTypes}

func recordHintsValue(ctx context.Context, hints []kinsta.DNSRecordHint) (types.List, diag.Diagnostics) {
	type hint struct {
		Type  string `tfsdk:"type"`
		Name  string `tfsdk:"name"`
		Value string `tfsdk:"value"`
	}
	out := make([]hint, 0, len(hints))
	for _, h := range hints {
		out = append(out, hint{Type: h.Type, Name: h.Name, Value: h.Value})
	}
	return types.ListValueFrom(ctx, recordHintType, out)
}

// findEnvironment returns the site's environment with this id, or nil.
func findEnvironment(envs []kinsta.Environment, id string) *kinsta.Environment {
	for i := range envs {
		if envs[i].ID == id {
			return &envs[i]
		}
	}
	return nil
}
