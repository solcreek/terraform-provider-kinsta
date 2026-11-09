// SPDX-License-Identifier: MPL-2.0

package kinsta

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// MinTTL is the smallest TTL Kinsta DNS accepts.
const MinTTL = 300

// Zone is a domain hosted on Kinsta DNS.
type Zone struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	SiteID   *string `json:"site_id"`
	IsActive bool    `json:"is_active"`
}

// ResourceRecord is one value of a record.
type ResourceRecord struct {
	Value string `json:"value"`
}

// Record is a Kinsta DNS record set: every value of one name and type.
// The live API returns Name fully qualified with a trailing dot.
type Record struct {
	Type            string           `json:"type"`
	Name            string           `json:"name"`
	TTL             int64            `json:"ttl"`
	ResourceRecords []ResourceRecord `json:"resource_records"`
}

// Values returns the record's values.
func (r Record) Values() []string {
	out := make([]string, 0, len(r.ResourceRecords))
	for _, v := range r.ResourceRecords {
		out = append(out, v.Value)
	}
	return out
}

// RecordUpdate is the body of PUT /domains/{id}/dns-records: a diff of one
// record's values, plus an optional new TTL.
type RecordUpdate struct {
	Type    string           `json:"type"`
	Name    string           `json:"name"`
	TTL     int64            `json:"ttl,omitempty"`
	Added   []ResourceRecord `json:"new_resource_records,omitempty"`
	Removed []ResourceRecord `json:"removed_resource_records,omitempty"`
}

// ListZones returns the company's zones on Kinsta DNS.
func (c *Client) ListZones(ctx context.Context, companyID string) ([]Zone, error) {
	var out struct {
		Company struct {
			Domains []Zone `json:"domains"`
		} `json:"company"`
	}
	if err := c.do(ctx, http.MethodGet, "/domains", url.Values{"company": {companyID}}, nil, &out); err != nil {
		return nil, err
	}
	return out.Company.Domains, nil
}

// ListRecords returns every record of a zone. An unknown zone id answers 500,
// not 404; callers that need to tell the two apart should check ListZones.
func (c *Client) ListRecords(ctx context.Context, zoneID string) ([]Record, error) {
	var out struct {
		Domain struct {
			DNSRecords []Record `json:"dns_records"`
		} `json:"domain"`
	}
	if err := c.do(ctx, http.MethodGet, "/domains/"+url.PathEscape(zoneID)+"/dns-records", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Domain.DNSRecords, nil
}

// FindRecord returns the zone's record of this type and name, or nil.
func (c *Client) FindRecord(ctx context.Context, zoneID, typ, name string) (*Record, error) {
	records, err := c.ListRecords(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	typ, name = strings.ToUpper(typ), NormalizeName(name)
	for i := range records {
		if strings.ToUpper(records[i].Type) == typ && NormalizeName(records[i].Name) == name {
			return &records[i], nil
		}
	}
	return nil, nil
}

// CreateRecord creates a record and waits for it.
func (c *Client) CreateRecord(ctx context.Context, zoneID string, r Record) error {
	var a Accepted
	if err := c.do(ctx, http.MethodPost, "/domains/"+url.PathEscape(zoneID)+"/dns-records", nil, r, &a); err != nil {
		return err
	}
	_, err := c.settle(ctx, &a)
	return err
}

// UpdateRecord applies a value diff (and TTL) to a record and waits.
func (c *Client) UpdateRecord(ctx context.Context, zoneID string, u RecordUpdate) error {
	var a Accepted
	if err := c.do(ctx, http.MethodPut, "/domains/"+url.PathEscape(zoneID)+"/dns-records", nil, u, &a); err != nil {
		return err
	}
	_, err := c.settle(ctx, &a)
	return err
}

// DeleteRecord deletes a record (all its values) and waits.
func (c *Client) DeleteRecord(ctx context.Context, zoneID, typ, name string) error {
	body := map[string]string{"type": typ, "name": name}
	var a Accepted
	if err := c.do(ctx, http.MethodDelete, "/domains/"+url.PathEscape(zoneID)+"/dns-records", nil, body, &a); err != nil {
		return err
	}
	_, err := c.settle(ctx, &a)
	return err
}

var txtChunk = regexp.MustCompile(`(?s).{1,255}`)

// EncodeValue turns a record value into the form Kinsta DNS stores. TXT (and
// SPF) data is kept in zone-file form — one or more quoted strings of at most
// 255 characters, read back as `"v=spf1 -all"` — so an unquoted value is
// quoted and split here. Every other type is stored as written.
func EncodeValue(typ, value string) string {
	switch strings.ToUpper(typ) {
	case "TXT", "SPF":
	default:
		return value
	}
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, `"`) {
		return trimmed
	}
	chunks := txtChunk.FindAllString(trimmed, -1)
	if len(chunks) == 0 {
		chunks = []string{""}
	}
	for i, c := range chunks {
		c = strings.ReplaceAll(c, `\`, `\\`)
		c = strings.ReplaceAll(c, `"`, `\"`)
		chunks[i] = `"` + c + `"`
	}
	return strings.Join(chunks, " ")
}

// DecodeValue is the inverse of EncodeValue for values that round-trip: a
// stored TXT value such as `"v=spf1 -all"` reads back as `v=spf1 -all`. A
// value whose bare form would not encode to the same stored text (say, one
// split at a place other than 255 characters) is returned unchanged.
func DecodeValue(typ, stored string) string {
	switch strings.ToUpper(typ) {
	case "TXT", "SPF":
	default:
		return stored
	}
	var (
		out     strings.Builder
		inQuote bool
		escaped bool
	)
	for _, r := range stored {
		switch {
		case escaped:
			out.WriteRune(r)
			escaped = false
		case inQuote && r == '\\':
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case inQuote:
			out.WriteRune(r)
		case r == ' ':
			// separator between strings
		default:
			return stored // not zone-file form
		}
	}
	if inQuote || escaped {
		return stored
	}
	bare := out.String()
	if bare == "" || strings.HasPrefix(strings.TrimSpace(bare), `"`) || bare != strings.TrimSpace(bare) || EncodeValue(typ, bare) != stored {
		return stored
	}
	return bare
}
