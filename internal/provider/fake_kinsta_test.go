// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const fakeCompany = "company-1"

// fakeKinsta is an in-memory stand-in for the parts of the Kinsta API the
// provider uses, so every resource's Terraform lifecycle can be tested
// without credentials. It mirrors behaviour observed on the live API
// (verified 2026-11-09): writes answer 202 with an operation id; operations
// report 202 before 200; record names come back with a trailing dot; an
// unknown zone answers 500 and an unknown site 404.
type fakeKinsta struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	next       int
	sites      map[string]*fakeSite
	zones      map[string]*fakeZone
	ops        map[string]*fakeOp
	opPolls    int // how many polls an operation stays in progress
	failMethod string
	requests   []string
}

type fakeSite struct {
	ID, Name, DisplayName, Status string
	Envs                          []*fakeEnv
}

type fakeEnv struct {
	ID, Name, DisplayName string
	IsPremium             bool
	PrimaryDomainID       string
	Domains               []fakeDomain
}

type fakeDomain struct{ ID, Name string }

type fakeZone struct {
	ID, Name string
	Records  []map[string]any // stored exactly as the API returns them
}

type fakeOp struct {
	polls int
	data  map[string]any
	fail  string
	apply func()
}

func newFakeKinsta(t *testing.T) *fakeKinsta {
	t.Helper()
	t.Setenv("KINSTA_POLL_INTERVAL_MS", "5")
	for _, k := range []string{"KINSTA_API_KEY", "KINSTA_COMPANY_ID", "KINSTA_API_URL"} {
		t.Setenv(k, "")
	}
	f := &fakeKinsta{t: t, sites: map[string]*fakeSite{}, zones: map[string]*fakeZone{}, ops: map[string]*fakeOp{}, opPolls: 1}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKinsta) providerConfig() string {
	return requiredProviders + fmt.Sprintf(`
provider "kinsta" {
  api_key = "test"
  api_url = %q
}
`, f.srv.URL)
}

func (f *fakeKinsta) id(prefix string) string {
	f.next++
	return fmt.Sprintf("%s-%d", prefix, f.next)
}

// addSite inserts a site with a live environment, as if made in MyKinsta.
func (f *fakeKinsta) addSite(displayName, primaryDomain string) *fakeSite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addSiteLocked(displayName, primaryDomain)
}

func (f *fakeKinsta) addSiteLocked(displayName, primaryDomain string) *fakeSite {
	name := strings.ToLower(strings.NewReplacer(" ", "", ".", "").Replace(displayName))
	s := &fakeSite{ID: f.id("site"), Name: name, DisplayName: displayName, Status: "live"}
	env := &fakeEnv{ID: f.id("env"), Name: "live", DisplayName: "Live", IsPremium: true}
	d := fakeDomain{ID: f.id("dom"), Name: name + ".kinsta.cloud"}
	env.Domains = append(env.Domains, d)
	env.PrimaryDomainID = d.ID
	if primaryDomain != "" {
		p := fakeDomain{ID: f.id("dom"), Name: primaryDomain}
		env.Domains = append(env.Domains, p)
		env.PrimaryDomainID = p.ID
	}
	s.Envs = []*fakeEnv{env}
	f.sites[s.ID] = s
	return s
}

func (f *fakeKinsta) addZone(name string) *fakeZone {
	f.mu.Lock()
	defer f.mu.Unlock()
	z := &fakeZone{ID: f.id("zone"), Name: name}
	f.zones[z.ID] = z
	return z
}

// record returns a stored record's values and ttl, or ok=false.
func (f *fakeKinsta) record(zoneID, typ, fqdn string) (values []string, ttl int, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.zones[zoneID].Records {
		if r["type"] == typ && r["name"] == fqdn+"." {
			for _, v := range r["resource_records"].([]any) {
				values = append(values, v.(map[string]any)["value"].(string))
			}
			return values, r["ttl"].(int), true
		}
	}
	return nil, 0, false
}

func (f *fakeKinsta) setRecord(zoneID, typ, fqdn string, ttl int, values ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	z := f.zones[zoneID]
	rr := []any{}
	for _, v := range values {
		rr = append(rr, map[string]any{"value": v})
	}
	for i, r := range z.Records {
		if r["type"] == typ && r["name"] == fqdn+"." {
			z.Records = append(z.Records[:i], z.Records[i+1:]...)
			break
		}
	}
	z.Records = append(z.Records, map[string]any{"type": typ, "name": fqdn + ".", "ttl": ttl, "resource_records": rr})
}

func (f *fakeKinsta) deleteRecord(zoneID, typ, fqdn string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	z := f.zones[zoneID]
	for i, r := range z.Records {
		if r["type"] == typ && r["name"] == fqdn+"." {
			z.Records = append(z.Records[:i], z.Records[i+1:]...)
			return
		}
	}
}

func (f *fakeKinsta) site(id string) *fakeSite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sites[id]
}

func (f *fakeKinsta) siteByDisplayName(name string) *fakeSite {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sites {
		if s.DisplayName == name {
			return s
		}
	}
	return nil
}

func (f *fakeKinsta) failRequests(method string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failMethod = method
}

// startOp registers an operation whose effect lands when it finishes.
func (f *fakeKinsta) startOp(w http.ResponseWriter, data map[string]any, apply func()) {
	id := f.id("op")
	f.ops[id] = &fakeOp{polls: f.opPolls, data: data, apply: apply}
	f.json(w, http.StatusAccepted, map[string]any{"operation_id": id, "message": "Operation in progress", "status": 202})
}

func (f *fakeKinsta) env(id string) (*fakeSite, *fakeEnv) {
	for _, s := range f.sites {
		for _, e := range s.Envs {
			if e.ID == id {
				return s, e
			}
		}
	}
	return nil, nil
}

func envJSON(e *fakeEnv) map[string]any {
	doms := []any{}
	var primary any
	for _, d := range e.Domains {
		ref := map[string]any{"id": d.ID, "name": d.Name, "type": e.Name}
		doms = append(doms, ref)
		if d.ID == e.PrimaryDomainID {
			primary = ref
		}
	}
	return map[string]any{
		"id": e.ID, "name": e.Name, "display_name": e.DisplayName, "is_premium": e.IsPremium, "is_blocked": false,
		"web_root": "/www/" + e.ID + "/public", "domains": doms, "primaryDomain": primary,
		"container_info": map[string]any{"id": "c-" + e.ID, "php_engine_version": "php8.3"},
	}
}

func siteJSON(s *fakeSite) map[string]any {
	envs := []any{}
	for _, e := range s.Envs {
		envs = append(envs, envJSON(e))
	}
	return map[string]any{"id": s.ID, "name": s.Name, "display_name": s.DisplayName, "status": s.Status,
		"company_id": fakeCompany, "environments": envs}
}

func (f *fakeKinsta) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)

	if r.Header.Get("Authorization") != "Bearer test" {
		f.json(w, http.StatusUnauthorized, map[string]any{"status": 401, "message": "No or invalid API key provided"})
		return
	}
	if f.failMethod != "" && r.Method == f.failMethod {
		f.json(w, http.StatusBadRequest, map[string]any{"status": 400, "message": "injected failure"})
		return
	}
	var body map[string]any
	if r.Body != nil && r.Method != http.MethodGet {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	str := func(k string) string { s, _ := body[k].(string); return s }
	boolean := func(k string) bool { b, _ := body[k].(bool); return b }
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	route := r.Method + " " + r.URL.Path

	switch {
	case route == "GET /validate":
		f.json(w, 200, map[string]any{"name": "test", "company": fakeCompany, "status": "active"})

	case route == "GET /company/"+fakeCompany+"/available-regions":
		f.json(w, 200, map[string]any{"company": map[string]any{"available_regions": []any{
			map[string]any{"name": "Ashburn (US)", "region": "us-ashburn-1"},
			map[string]any{"name": "Tokyo (JP)", "region": "ap-tokyo-1"},
		}}})

	case route == "GET /operations/"+lastPart(p):
		op, ok := f.ops[lastPart(p)]
		switch {
		case !ok:
			f.json(w, 404, map[string]any{"status": 404, "message": "Operation not found!"})
		case op.polls > 0:
			op.polls--
			f.json(w, 202, map[string]any{"status": 202, "message": "Operation in progress"})
		case op.fail != "":
			f.json(w, 500, map[string]any{"status": 500, "message": "Operation failed!", "data": map[string]any{"message": op.fail}})
		default:
			if op.apply != nil {
				op.apply()
				op.apply = nil
			}
			f.json(w, 200, map[string]any{"status": 200, "message": "Successfully finished request.", "data": op.data})
		}

	// ---- sites
	case route == "GET /sites":
		if r.URL.Query().Get("company") != fakeCompany {
			f.json(w, 400, map[string]any{"status": 400, "message": "company is required"})
			return
		}
		list := []any{}
		for _, s := range f.sites {
			list = append(list, siteJSON(s))
		}
		f.json(w, 200, map[string]any{"company": map[string]any{"sites": list}})
	case route == "POST /sites" || route == "POST /sites/plain":
		if str("company") != fakeCompany || str("region") == "" {
			f.json(w, 400, map[string]any{"status": 400, "message": "invalid body"})
			return
		}
		if route == "POST /sites" && (str("admin_password") == "" || str("install_mode") != "new") {
			f.json(w, 400, map[string]any{"status": 400, "message": "admin_password is required"})
			return
		}
		displayName := str("display_name")
		var created *fakeSite
		data := map[string]any{}
		f.startOp(w, data, func() { created = f.addSiteLocked(displayName, ""); data["idSite"] = created.ID })
	case len(p) == 2 && p[0] == "sites" && r.Method == http.MethodGet:
		s, ok := f.sites[p[1]]
		if !ok {
			f.json(w, 404, map[string]any{"status": 404, "message": "The requested site was not found!"})
			return
		}
		f.json(w, 200, map[string]any{"site": siteJSON(s)})
	case len(p) == 2 && p[0] == "sites" && r.Method == http.MethodDelete:
		id := p[1]
		if _, ok := f.sites[id]; !ok {
			f.json(w, 404, map[string]any{"status": 404, "message": "The requested site was not found!"})
			return
		}
		f.startOp(w, nil, func() { delete(f.sites, id) })
	case len(p) == 3 && p[0] == "sites" && p[2] == "environments" && r.Method == http.MethodGet:
		s, ok := f.sites[p[1]]
		if !ok {
			f.json(w, 404, map[string]any{"status": 404, "message": "The requested site was not found!"})
			return
		}
		envs := []any{}
		for _, e := range s.Envs {
			envs = append(envs, envJSON(e))
		}
		f.json(w, 200, map[string]any{"site": map[string]any{"environments": envs}})
	case len(p) >= 3 && p[0] == "sites" && p[2] == "environments" && r.Method == http.MethodPost:
		s, ok := f.sites[p[1]]
		if !ok {
			f.json(w, 404, map[string]any{"status": 404, "message": "The requested site was not found!"})
			return
		}
		displayName, premium := str("display_name"), boolean("is_premium")
		// Like the live API (as far as observed), no environment id is reported.
		f.startOp(w, nil, func() {
			e := &fakeEnv{ID: f.id("env"), Name: strings.ToLower(displayName), DisplayName: displayName, IsPremium: premium}
			d := fakeDomain{ID: f.id("dom"), Name: "env-" + s.Name + "-" + e.Name + ".kinsta.cloud"}
			e.Domains, e.PrimaryDomainID = []fakeDomain{d}, d.ID
			s.Envs = append(s.Envs, e)
		})
	case len(p) == 3 && p[0] == "sites" && p[1] == "environments" && r.Method == http.MethodDelete:
		s, e := f.env(p[2])
		if e == nil {
			f.json(w, 404, map[string]any{"status": 404, "message": "Environment not found"})
			return
		}
		f.startOp(w, nil, func() {
			for i, x := range s.Envs {
				if x.ID == e.ID {
					s.Envs = append(s.Envs[:i], s.Envs[i+1:]...)
					return
				}
			}
		})

	// ---- site domains
	case len(p) == 4 && p[1] == "environments" && p[3] == "domains":
		_, e := f.env(p[2])
		if e == nil {
			f.json(w, 404, map[string]any{"status": 404, "message": "Environment not found"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			list := []any{}
			for _, d := range e.Domains {
				list = append(list, map[string]any{"id": d.ID, "name": d.Name, "uses_cloudflare_dns": strings.HasSuffix(d.Name, ".kinsta.cloud")})
			}
			f.json(w, 200, map[string]any{"environment": map[string]any{"site_domains": list}})
		case http.MethodPost:
			name := str("domain_name")
			if !boolean("is_wildcardless") {
				f.t.Errorf("fake kinsta: domain %s added with a wildcard", name)
			}
			f.startOp(w, nil, func() { e.Domains = append(e.Domains, fakeDomain{ID: f.id("dom"), Name: name}) })
		case http.MethodDelete:
			ids, _ := body["domain_ids"].([]any)
			f.startOp(w, nil, func() {
				for _, id := range ids {
					if id == e.PrimaryDomainID {
						continue // the primary domain cannot be removed
					}
					for i, d := range e.Domains {
						if d.ID == id {
							e.Domains = append(e.Domains[:i], e.Domains[i+1:]...)
							break
						}
					}
				}
			})
		}
	case len(p) == 5 && p[2] == "domains" && p[4] == "verification-records" && r.Method == http.MethodGet:
		var name string
		for _, s := range f.sites {
			for _, e := range s.Envs {
				for _, d := range e.Domains {
					if d.ID == p[3] {
						name = d.Name
					}
				}
			}
		}
		if name == "" {
			f.json(w, 404, map[string]any{"status": 404, "message": "Domain not found"})
			return
		}
		ver, point := []any{}, []any{}
		if !strings.HasSuffix(name, ".kinsta.cloud") {
			ver = append(ver, map[string]any{"type": "TXT", "name": "kinsta-verification." + name, "value": "verify-" + p[3]})
			point = append(point, map[string]any{"type": "CNAME", "name": name, "value": "sites.kinsta.cloud"})
		}
		f.json(w, 200, map[string]any{"site_domain": map[string]any{"verification_records": ver, "pointing_records": point}})
	case len(p) == 4 && p[3] == "change-primary-domain" && r.Method == http.MethodPut:
		_, e := f.env(p[2])
		if e == nil {
			f.json(w, 404, map[string]any{"status": 404, "message": "Environment not found"})
			return
		}
		id := str("domain_id")
		f.startOp(w, nil, func() { e.PrimaryDomainID = id })

	// ---- Kinsta DNS
	case route == "GET /domains":
		list := []any{}
		for _, z := range f.zones {
			list = append(list, map[string]any{"id": z.ID, "name": z.Name, "site_id": nil, "is_active": true})
		}
		f.json(w, 200, map[string]any{"company": map[string]any{"domains": list}})
	case len(p) == 3 && p[0] == "domains" && p[2] == "dns-records":
		z, ok := f.zones[p[1]]
		if !ok {
			f.json(w, 500, map[string]any{"status": 500, "message": "Server Error"})
			return
		}
		f.serveRecords(w, r.Method, z, body)

	default:
		f.json(w, 404, map[string]any{"status": 404, "message": "fake kinsta: no route for " + route})
	}
}

func (f *fakeKinsta) serveRecords(w http.ResponseWriter, method string, z *fakeZone, body map[string]any) {
	typ, _ := body["type"].(string)
	name, _ := body["name"].(string)
	stored := strings.TrimSuffix(name, ".") + "."
	find := func() int {
		for i, rec := range z.Records {
			if rec["type"] == typ && rec["name"] == stored {
				return i
			}
		}
		return -1
	}
	switch method {
	case http.MethodGet:
		f.json(w, 200, map[string]any{"domain": map[string]any{"dns_records": z.Records}})
	case http.MethodPost:
		if find() >= 0 {
			f.json(w, 400, map[string]any{"status": 400, "message": "Record already exists"})
			return
		}
		ttl := int(body["ttl"].(float64))
		rr := body["resource_records"]
		// DNS writes answer 202 without an operation id: the record lands
		// on the next read.
		f.json(w, 202, map[string]any{"message": "DNS record created", "status": 202})
		z.Records = append(z.Records, map[string]any{"type": typ, "name": stored, "ttl": ttl, "resource_records": rr})
	case http.MethodPut:
		i := find()
		if i < 0 || name != stored {
			// The live API only matches the stored name, trailing dot included.
			f.json(w, 400, map[string]any{"status": 400, "message": "Record not found"})
			return
		}
		rec := z.Records[i]
		if ttl, ok := body["ttl"].(float64); ok {
			rec["ttl"] = int(ttl)
		}
		values := rec["resource_records"].([]any)
		removed := map[string]bool{}
		if rm, ok := body["removed_resource_records"].([]any); ok {
			for _, v := range rm {
				removed[v.(map[string]any)["value"].(string)] = true
			}
		}
		kept := []any{}
		for _, v := range values {
			if !removed[v.(map[string]any)["value"].(string)] {
				kept = append(kept, v)
			}
		}
		if add, ok := body["new_resource_records"].([]any); ok {
			kept = append(kept, add...)
		}
		rec["resource_records"] = kept
		f.startOp(w, nil, nil)
	case http.MethodDelete:
		i := find()
		if i < 0 {
			f.json(w, 404, map[string]any{"status": 404, "message": "Record not found"})
			return
		}
		z.Records = append(z.Records[:i], z.Records[i+1:]...)
		f.startOp(w, nil, nil)
	}
}

func lastPart(p []string) string { return p[len(p)-1] }

func (f *fakeKinsta) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
