// SPDX-License-Identifier: MPL-2.0

package kinsta

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// SiteDomain is a domain attached to an environment.
type SiteDomain struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	UsesCloudflareDNS bool   `json:"uses_cloudflare_dns"`
}

// DNSRecordHint is a record Kinsta asks for, to verify or point a site domain.
type DNSRecordHint struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SiteDomainRecords are the records a site domain still needs.
type SiteDomainRecords struct {
	Verification []DNSRecordHint `json:"verification_records"`
	Pointing     []DNSRecordHint `json:"pointing_records"`
}

// ListSiteDomains returns the domains of an environment.
func (c *Client) ListSiteDomains(ctx context.Context, envID string) ([]SiteDomain, error) {
	var out struct {
		Environment struct {
			SiteDomains []SiteDomain `json:"site_domains"`
		} `json:"environment"`
	}
	if err := c.do(ctx, http.MethodGet, "/sites/environments/"+url.PathEscape(envID)+"/domains", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Environment.SiteDomains, nil
}

// FindSiteDomain returns the environment's domain with this name, or nil.
func (c *Client) FindSiteDomain(ctx context.Context, envID, name string) (*SiteDomain, error) {
	domains, err := c.ListSiteDomains(ctx, envID)
	if err != nil {
		return nil, err
	}
	want := NormalizeName(name)
	for i := range domains {
		if NormalizeName(domains[i].Name) == want {
			return &domains[i], nil
		}
	}
	return nil, nil
}

// AddSiteDomain adds one domain to an environment and waits. The domain is
// added without a wildcard: the API refuses a www companion on a
// wildcardless domain, so a www host is added as a domain of its own.
func (c *Client) AddSiteDomain(ctx context.Context, envID, name string) error {
	body := map[string]any{"domain_name": name, "is_wildcardless": true}
	var a Accepted
	if err := c.do(ctx, http.MethodPost, "/sites/environments/"+url.PathEscape(envID)+"/domains", nil, body, &a); err != nil {
		return err
	}
	_, err := c.settle(ctx, &a)
	return err
}

// DeleteSiteDomains removes domains from an environment and waits.
func (c *Client) DeleteSiteDomains(ctx context.Context, envID string, domainIDs ...string) error {
	body := map[string]any{"domain_ids": domainIDs}
	var a Accepted
	if err := c.do(ctx, http.MethodDelete, "/sites/environments/"+url.PathEscape(envID)+"/domains", nil, body, &a); err != nil {
		return err
	}
	_, err := c.settle(ctx, &a)
	return err
}

// GetSiteDomainRecords returns the verification and pointing records a site
// domain still needs. Both lists are empty once the domain is verified.
func (c *Client) GetSiteDomainRecords(ctx context.Context, siteDomainID string) (*SiteDomainRecords, error) {
	var out struct {
		SiteDomain SiteDomainRecords `json:"site_domain"`
	}
	if err := c.do(ctx, http.MethodGet, "/sites/environments/domains/"+url.PathEscape(siteDomainID)+"/verification-records", nil, nil, &out); err != nil {
		return nil, err
	}
	if out.SiteDomain.Verification == nil {
		out.SiteDomain.Verification = []DNSRecordHint{}
	}
	if out.SiteDomain.Pointing == nil {
		out.SiteDomain.Pointing = []DNSRecordHint{}
	}
	return &out.SiteDomain, nil
}

// ChangePrimaryDomain makes a site domain the environment's primary domain
// and waits. With searchReplace, Kinsta rewrites the old URL in the database.
func (c *Client) ChangePrimaryDomain(ctx context.Context, envID, siteDomainID string, searchReplace bool) error {
	body := map[string]any{"domain_id": siteDomainID, "run_search_and_replace": searchReplace}
	var a Accepted
	if err := c.do(ctx, http.MethodPut, "/sites/environments/"+url.PathEscape(envID)+"/change-primary-domain", nil, body, &a); err != nil {
		return err
	}
	_, err := c.settle(ctx, &a)
	return err
}

// NormalizeName lowercases a DNS name and drops a trailing dot.
func NormalizeName(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}
