// SPDX-License-Identifier: MPL-2.0

package kinsta

import (
	"context"
	"net/http"
	"net/url"
)

// KeyInfo is the response of GET /validate.
type KeyInfo struct {
	Name      string  `json:"name"`
	Company   string  `json:"company"`
	Status    string  `json:"status"`
	ExpiresAt *string `json:"expires_at"`
}

// Validate checks the API key and returns the company it belongs to.
func (c *Client) Validate(ctx context.Context) (*KeyInfo, error) {
	var out KeyInfo
	if err := c.do(ctx, http.MethodGet, "/validate", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Region is a data center a site can be created in.
type Region struct {
	Name   string `json:"name"`
	Region string `json:"region"`
}

// ListRegions returns the regions available to a company.
func (c *Client) ListRegions(ctx context.Context, companyID string) ([]Region, error) {
	var out struct {
		Company struct {
			AvailableRegions []Region `json:"available_regions"`
		} `json:"company"`
	}
	if err := c.do(ctx, http.MethodGet, "/company/"+url.PathEscape(companyID)+"/available-regions", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Company.AvailableRegions, nil
}
