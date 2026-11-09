// SPDX-License-Identifier: MPL-2.0

package kinsta

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// DomainRef is a domain as embedded in an environment.
type DomainRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// ContainerInfo is an environment's runtime container.
type ContainerInfo struct {
	ID               string `json:"id"`
	PHPEngineVersion string `json:"php_engine_version"`
}

// Environment is a WordPress site environment (live or staging).
type Environment struct {
	ID                    string         `json:"id"`
	Name                  string         `json:"name"`
	DisplayName           string         `json:"display_name"`
	IsPremium             bool           `json:"is_premium"`
	IsBlocked             bool           `json:"is_blocked"`
	WebRoot               string         `json:"web_root"`
	WordPressVersion      *string        `json:"wordpress_version"`
	PrimaryDomain         *DomainRef     `json:"primaryDomain"`
	Domains               []DomainRef    `json:"domains"`
	ContainerInfo         *ContainerInfo `json:"container_info"`
	ImageOptimization     *string        `json:"image_optimization_type"`
	OptOutOfAutoPHPUpdate *bool          `json:"is_opt_out_from_automatic_php_update"`
}

// IsLive reports whether this is a site's live (production) environment.
func (e Environment) IsLive() bool { return e.Name == "live" }

// PHPVersion returns the PHP version without Kinsta's `php` prefix
// (the API reports `php8.3`), or "" when unknown.
func (e Environment) PHPVersion() string {
	if e.ContainerInfo == nil {
		return ""
	}
	return strings.TrimPrefix(e.ContainerInfo.PHPEngineVersion, "php")
}

// Site is a WordPress site.
type Site struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	DisplayName  string        `json:"display_name"`
	Status       string        `json:"status"`
	CompanyID    string        `json:"company_id"`
	Environments []Environment `json:"environments"`
}

// LiveEnvironment returns the site's live environment, if listed.
func (s Site) LiveEnvironment() *Environment {
	for i := range s.Environments {
		if s.Environments[i].IsLive() {
			return &s.Environments[i]
		}
	}
	return nil
}

// ListSites returns every site of a company, with environments.
func (c *Client) ListSites(ctx context.Context, companyID string) ([]Site, error) {
	var out struct {
		Company struct {
			Sites []Site `json:"sites"`
		} `json:"company"`
	}
	q := url.Values{"company": {companyID}, "include_environments": {"true"}}
	if err := c.do(ctx, http.MethodGet, "/sites", q, nil, &out); err != nil {
		return nil, err
	}
	return out.Company.Sites, nil
}

// GetSite returns one site. Its environments carry ids, names and the primary
// domain but not the container details; use ListEnvironments for those.
func (c *Client) GetSite(ctx context.Context, siteID string) (*Site, error) {
	var out struct {
		Site Site `json:"site"`
	}
	if err := c.do(ctx, http.MethodGet, "/sites/"+url.PathEscape(siteID), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out.Site, nil
}

// ListEnvironments returns a site's environments with full details.
func (c *Client) ListEnvironments(ctx context.Context, siteID string) ([]Environment, error) {
	var out struct {
		Site struct {
			Environments []Environment `json:"environments"`
		} `json:"site"`
	}
	if err := c.do(ctx, http.MethodGet, "/sites/"+url.PathEscape(siteID)+"/environments", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Site.Environments, nil
}

// CreateSiteRequest is the body of POST /sites (a full WordPress install).
type CreateSiteRequest struct {
	Company              string `json:"company"`
	DisplayName          string `json:"display_name"`
	Region               string `json:"region"`
	InstallMode          string `json:"install_mode"`
	AdminEmail           string `json:"admin_email"`
	AdminPassword        string `json:"admin_password"`
	AdminUser            string `json:"admin_user"`
	SiteTitle            string `json:"site_title"`
	WPLanguage           string `json:"wp_language"`
	IsMultisite          bool   `json:"is_multisite"`
	IsSubdomainMultisite bool   `json:"is_subdomain_multisite"`
	WooCommerce          bool   `json:"woocommerce"`
	WordPressSEO         bool   `json:"wordpressseo"`
}

// CreatePlainSiteRequest is the body of POST /sites/plain (no WordPress).
type CreatePlainSiteRequest struct {
	Company     string `json:"company"`
	DisplayName string `json:"display_name"`
	Region      string `json:"region"`
}

// CreateSite starts a WordPress site installation and waits for it. The
// returned operation may carry the new site's id in data.idSite.
func (c *Client) CreateSite(ctx context.Context, req CreateSiteRequest) (*Operation, error) {
	var a Accepted
	if err := c.do(ctx, http.MethodPost, "/sites", nil, req, &a); err != nil {
		return nil, err
	}
	return c.settle(ctx, &a)
}

// CreatePlainSite starts an empty site (no WordPress installed) and waits.
func (c *Client) CreatePlainSite(ctx context.Context, req CreatePlainSiteRequest) (*Operation, error) {
	var a Accepted
	if err := c.do(ctx, http.MethodPost, "/sites/plain", nil, req, &a); err != nil {
		return nil, err
	}
	return c.settle(ctx, &a)
}

// DeleteSite deletes a site and every environment, and waits.
func (c *Client) DeleteSite(ctx context.Context, siteID string) error {
	var a Accepted
	if err := c.do(ctx, http.MethodDelete, "/sites/"+url.PathEscape(siteID), nil, nil, &a); err != nil {
		return err
	}
	_, err := c.settle(ctx, &a)
	return err
}

// CreateEnvironmentRequest is the body of POST /sites/{site_id}/environments.
type CreateEnvironmentRequest struct {
	DisplayName          string `json:"display_name"`
	IsPremium            bool   `json:"is_premium"`
	SiteTitle            string `json:"site_title"`
	AdminEmail           string `json:"admin_email"`
	AdminPassword        string `json:"admin_password"`
	AdminUser            string `json:"admin_user"`
	WPLanguage           string `json:"wp_language"`
	IsMultisite          bool   `json:"is_multisite"`
	IsSubdomainMultisite bool   `json:"is_subdomain_multisite"`
	WooCommerce          bool   `json:"woocommerce"`
	WordPressSEO         bool   `json:"wordpressseo"`
}

// CreatePlainEnvironmentRequest is the body of POST /sites/{site_id}/environments/plain.
type CreatePlainEnvironmentRequest struct {
	DisplayName string `json:"display_name"`
	IsPremium   bool   `json:"is_premium"`
}

// CreateEnvironment adds a WordPress environment to a site and waits.
func (c *Client) CreateEnvironment(ctx context.Context, siteID string, req CreateEnvironmentRequest) (*Operation, error) {
	var a Accepted
	if err := c.do(ctx, http.MethodPost, "/sites/"+url.PathEscape(siteID)+"/environments", nil, req, &a); err != nil {
		return nil, err
	}
	return c.settle(ctx, &a)
}

// CreatePlainEnvironment adds an empty environment to a site and waits.
func (c *Client) CreatePlainEnvironment(ctx context.Context, siteID string, req CreatePlainEnvironmentRequest) (*Operation, error) {
	var a Accepted
	if err := c.do(ctx, http.MethodPost, "/sites/"+url.PathEscape(siteID)+"/environments/plain", nil, req, &a); err != nil {
		return nil, err
	}
	return c.settle(ctx, &a)
}

// DeleteEnvironment deletes a (non-live) environment and waits.
func (c *Client) DeleteEnvironment(ctx context.Context, envID string) error {
	var a Accepted
	if err := c.do(ctx, http.MethodDelete, "/sites/environments/"+url.PathEscape(envID), nil, nil, &a); err != nil {
		return err
	}
	_, err := c.settle(ctx, &a)
	return err
}

// ModifyPHPVersion changes an environment's PHP version (e.g. "8.3") and waits.
func (c *Client) ModifyPHPVersion(ctx context.Context, envID, version string, optOutOfAutoUpdate *bool) error {
	body := map[string]any{"environment_id": envID, "php_version": version}
	if optOutOfAutoUpdate != nil {
		body["is_opt_out_from_automatic_php_update"] = *optOutOfAutoUpdate
	}
	var a Accepted
	if err := c.do(ctx, http.MethodPut, "/sites/tools/modify-php-version", nil, body, &a); err != nil {
		return err
	}
	_, err := c.settle(ctx, &a)
	return err
}
