// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/solcreek/terraform-provider-kinsta/internal/kinsta"
)

var (
	_ resource.Resource                = (*dnsRecordResource)(nil)
	_ resource.ResourceWithConfigure   = (*dnsRecordResource)(nil)
	_ resource.ResourceWithImportState = (*dnsRecordResource)(nil)
)

type dnsRecordResource struct {
	data *providerData
}

func newDNSRecordResource() resource.Resource { return &dnsRecordResource{} }

type dnsRecordModel struct {
	ID       types.String `tfsdk:"id"`
	ZoneID   types.String `tfsdk:"zone_id"`
	ZoneName types.String `tfsdk:"zone_name"`
	Name     types.String `tfsdk:"name"`
	FQDN     types.String `tfsdk:"fqdn"`
	Type     types.String `tfsdk:"type"`
	Values   types.Set    `tfsdk:"values"`
	TTL      types.Int64  `tfsdk:"ttl"`
}

func (r *dnsRecordResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_record"
}

func (r *dnsRecordResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages one record set on Kinsta DNS: every value of one name and type in a zone. " +
			"Use the `kinsta_dns_zone` data source to look up a zone's ID by name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`<zone_id>/<type>/<fqdn>`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"zone_id": schema.StringAttribute{
				MarkdownDescription: "ID of the Kinsta DNS zone.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"zone_name": schema.StringAttribute{
				MarkdownDescription: "Name of the zone, e.g. `example.com`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Record name: `@` for the zone apex, a name relative to the zone (`www`), or a fully " +
					"qualified name inside the zone (`www.example.com`). A trailing dot marks a name as absolute. " +
					"Changing it to another spelling of the same name (say `www` to `www.example.com`) does not replace the record.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					sameFQDNModifier{},
				},
			},
			"fqdn": schema.StringAttribute{
				MarkdownDescription: "Fully qualified record name, lowercase and without the trailing dot.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "Record type in upper case, e.g. `A`, `AAAA`, `CNAME`, `MX`, `TXT`, `SRV` or `CAA`.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`^[A-Z0-9]+$`), "must be an upper-case record type such as A or TXT"),
				},
			},
			"values": schema.SetAttribute{
				MarkdownDescription: "The record's values, in zone-file form: `10 mail.example.com` for MX, " +
					"`10 5 443 target.example.com` for SRV. TXT values may be written bare (`v=spf1 -all`): " +
					"they are quoted and split into 255-character strings the way Kinsta DNS stores them, and a " +
					"bare value and its quoted form are treated as equal.",
				ElementType: types.StringType,
				Required:    true,
				Validators:  []validator.Set{setvalidator.SizeAtLeast(1)},
			},
			"ttl": schema.Int64Attribute{
				MarkdownDescription: fmt.Sprintf("TTL in seconds, at least %d. Defaults to 3600.", kinsta.MinTTL),
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(3600),
				Validators:          []validator.Int64{int64validator.AtLeast(kinsta.MinTTL)},
			},
		},
	}
}

func (r *dnsRecordResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = configureData(req.ProviderData, &resp.Diagnostics)
}

// zoneByID returns the zone, or nil when the company has no such zone.
func (r *dnsRecordResource) zoneByID(ctx context.Context, id string) (*kinsta.Zone, error) {
	zones, err := r.data.client.ListZones(ctx, r.data.companyID)
	if err != nil {
		return nil, err
	}
	for i := range zones {
		if zones[i].ID == id {
			return &zones[i], nil
		}
	}
	return nil, nil
}

func (r *dnsRecordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dnsRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, dnsOperationTimeout)
	defer cancel()

	zoneID := plan.ZoneID.ValueString()
	zone, err := r.zoneByID(ctx, zoneID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read Kinsta DNS zones", err.Error())
		return
	}
	if zone == nil {
		resp.Diagnostics.AddAttributeError(path.Root("zone_id"), "Unknown zone",
			fmt.Sprintf("Zone %q is not a Kinsta DNS zone of company %s.", zoneID, r.data.companyID))
		return
	}

	typ := plan.Type.ValueString()
	fqdn := qualifyName(plan.Name.ValueString(), zone.Name)
	if !inZone(fqdn, zone.Name) {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Name outside the zone",
			fmt.Sprintf("%s is outside the zone %s.", fqdn, zone.Name))
		return
	}

	var values []string
	resp.Diagnostics.Append(plan.Values.ElementsAs(ctx, &values, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	existing, err := r.data.client.FindRecord(ctx, zoneID, typ, fqdn)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read DNS records", err.Error())
		return
	}
	if existing != nil {
		resp.Diagnostics.AddError("DNS record already exists",
			fmt.Sprintf("%s %s already exists in %s with values %v. Import it instead: terraform import <address> %s",
				typ, fqdn, zone.Name, existing.Values(), recordID(zoneID, typ, fqdn)))
		return
	}

	encoded := encodeValues(typ, values)
	rec := kinsta.Record{Type: typ, Name: fqdn, TTL: plan.TTL.ValueInt64()}
	for _, v := range encoded {
		rec.ResourceRecords = append(rec.ResourceRecords, kinsta.ResourceRecord{Value: v})
	}
	if err := r.data.client.CreateRecord(ctx, zoneID, rec); err != nil {
		resp.Diagnostics.AddError("Unable to create DNS record", err.Error())
		return
	}

	got, err := r.waitForRecord(ctx, zoneID, typ, fqdn, encoded, plan.TTL.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError("DNS record did not appear", err.Error())
		return
	}

	plan.ID = types.StringValue(recordID(zoneID, typ, fqdn))
	plan.ZoneName = types.StringValue(kinsta.NormalizeName(zone.Name))
	plan.FQDN = types.StringValue(fqdn)
	resp.Diagnostics.Append(r.setFromRecord(ctx, &plan, got, values)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dnsRecordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zoneID := state.ZoneID.ValueString()
	rec, err := r.data.client.FindRecord(ctx, zoneID, state.Type.ValueString(), state.FQDN.ValueString())
	if err != nil {
		// An unknown zone id answers 500 rather than 404: check whether the
		// zone is gone before reporting an error.
		if kinsta.StatusCode(err) == http.StatusInternalServerError || kinsta.IsNotFound(err) {
			if zone, zerr := r.zoneByID(ctx, zoneID); zerr == nil && zone == nil {
				resp.State.RemoveResource(ctx)
				return
			}
		}
		resp.Diagnostics.AddError("Unable to read DNS record", err.Error())
		return
	}
	if rec == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	if state.ZoneName.IsNull() || state.ZoneName.ValueString() == "" {
		zone, err := r.zoneByID(ctx, zoneID)
		if err != nil {
			resp.Diagnostics.AddError("Unable to read Kinsta DNS zones", err.Error())
			return
		}
		if zone != nil {
			state.ZoneName = types.StringValue(kinsta.NormalizeName(zone.Name))
		}
	}

	var prior []string
	if !state.Values.IsNull() && !state.Values.IsUnknown() {
		resp.Diagnostics.Append(state.Values.ElementsAs(ctx, &prior, false)...)
	}
	resp.Diagnostics.Append(r.setFromRecord(ctx, &state, rec, prior)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *dnsRecordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dnsRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, dnsOperationTimeout)
	defer cancel()

	zoneID, typ, fqdn := state.ZoneID.ValueString(), state.Type.ValueString(), state.FQDN.ValueString()
	var values []string
	resp.Diagnostics.Append(plan.Values.ElementsAs(ctx, &values, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	existing, err := r.data.client.FindRecord(ctx, zoneID, typ, fqdn)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read DNS record", err.Error())
		return
	}
	if existing == nil {
		resp.Diagnostics.AddError("DNS record disappeared",
			fmt.Sprintf("%s %s no longer exists in the zone; run `terraform apply` again to recreate it.", typ, fqdn))
		return
	}

	wanted := encodeValues(typ, values)
	added, removed := diffValues(existing.Values(), wanted)
	ttl := plan.TTL.ValueInt64()
	if len(added) > 0 || len(removed) > 0 || ttl != existing.TTL {
		// Send the name as the API stored it (with its trailing dot).
		u := kinsta.RecordUpdate{Type: existing.Type, Name: existing.Name, TTL: ttl}
		for _, v := range added {
			u.Added = append(u.Added, kinsta.ResourceRecord{Value: v})
		}
		for _, v := range removed {
			u.Removed = append(u.Removed, kinsta.ResourceRecord{Value: v})
		}
		if err := r.data.client.UpdateRecord(ctx, zoneID, u); err != nil {
			resp.Diagnostics.AddError("Unable to update DNS record", err.Error())
			return
		}
	}

	got, err := r.waitForRecord(ctx, zoneID, typ, fqdn, wanted, ttl)
	if err != nil {
		resp.Diagnostics.AddError("DNS record update did not land", err.Error())
		return
	}
	plan.ID, plan.ZoneName, plan.FQDN = state.ID, state.ZoneName, state.FQDN
	resp.Diagnostics.Append(r.setFromRecord(ctx, &plan, got, values)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dnsRecordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, dnsOperationTimeout)
	defer cancel()

	zoneID := state.ZoneID.ValueString()
	existing, err := r.data.client.FindRecord(ctx, zoneID, state.Type.ValueString(), state.FQDN.ValueString())
	if err != nil {
		if zone, zerr := r.zoneByID(ctx, zoneID); zerr == nil && zone == nil {
			return // the whole zone is gone
		}
		resp.Diagnostics.AddError("Unable to read DNS record", err.Error())
		return
	}
	if existing == nil {
		return
	}
	if err := r.data.client.DeleteRecord(ctx, zoneID, existing.Type, existing.Name); err != nil {
		if !kinsta.IsNotFound(err) {
			resp.Diagnostics.AddError("Unable to delete DNS record", err.Error())
		}
		return
	}
	if _, err := r.waitForRecord(ctx, zoneID, existing.Type, state.FQDN.ValueString(), nil, 0); err != nil {
		resp.Diagnostics.AddError("DNS record was not deleted in time", err.Error())
	}
}

func (r *dnsRecordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected `<zone_id>/<type>/<fqdn>` (for example `0757c118-.../TXT/_dmarc.example.com`), got %q.", req.ID))
		return
	}
	zoneID, typ, fqdn := parts[0], strings.ToUpper(parts[1]), kinsta.NormalizeName(parts[2])
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), recordID(zoneID, typ, fqdn))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), zoneID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("type"), typ)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), fqdn)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("fqdn"), fqdn)...)
}

// setFromRecord copies the API's view of a record into m. Values are reported
// in the spelling of prior (the configuration or the last state) whenever
// that spelling encodes to what the API holds, so a bare TXT value does not
// show a perpetual diff against its quoted stored form. Other values (an
// import, or drift) are reported in their bare form where that round-trips.
func (r *dnsRecordResource) setFromRecord(ctx context.Context, m *dnsRecordModel, rec *kinsta.Record, prior []string) (diags diag.Diagnostics) {
	typ := m.Type.ValueString()
	spelling := make(map[string]string, len(prior))
	for _, p := range prior {
		spelling[kinsta.EncodeValue(typ, p)] = p
	}
	values := make([]string, 0, len(rec.ResourceRecords))
	for _, v := range rec.Values() {
		if p, ok := spelling[v]; ok {
			values = append(values, p)
		} else {
			values = append(values, kinsta.DecodeValue(typ, v))
		}
	}
	set, d := types.SetValueFrom(ctx, types.StringType, values)
	diags = append(diags, d...)
	m.Values = set
	m.TTL = types.Int64Value(rec.TTL)
	return diags
}

// waitForRecord polls until the record holds exactly wanted with ttl, or —
// when wanted is nil — until the record is gone. Kinsta does not always
// return an operation to wait on, and a write can take a moment to show.
func (r *dnsRecordResource) waitForRecord(ctx context.Context, zoneID, typ, fqdn string, wanted []string, ttl int64) (*kinsta.Record, error) {
	const settleTimeout = 2 * time.Minute
	deadline := time.Now().Add(settleTimeout)
	for {
		rec, err := r.data.client.FindRecord(ctx, zoneID, typ, fqdn)
		if err != nil {
			return nil, err
		}
		if wanted == nil && rec == nil {
			return nil, nil
		}
		if wanted != nil && rec != nil && rec.TTL == ttl {
			if added, removed := diffValues(rec.Values(), wanted); len(added) == 0 && len(removed) == 0 {
				return rec, nil
			}
		}
		if time.Now().After(deadline) {
			if wanted == nil {
				return nil, fmt.Errorf("%s %s still exists %s after the delete was accepted", typ, fqdn, settleTimeout)
			}
			var got any = "nothing"
			if rec != nil {
				got = fmt.Sprintf("%v (ttl %d)", rec.Values(), rec.TTL)
			}
			return nil, fmt.Errorf("%s %s holds %v, want %v (ttl %d), %s after the write was accepted", typ, fqdn, got, wanted, ttl, settleTimeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval()):
		}
	}
}

func recordID(zoneID, typ, fqdn string) string { return zoneID + "/" + typ + "/" + fqdn }

func encodeValues(typ string, values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		e := kinsta.EncodeValue(typ, v)
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

// diffValues returns the values to add to current and to remove from it so
// that it holds exactly wanted.
func diffValues(current, wanted []string) (added, removed []string) {
	cur := map[string]bool{}
	for _, v := range current {
		cur[v] = true
	}
	want := map[string]bool{}
	for _, v := range wanted {
		want[v] = true
		if !cur[v] {
			added = append(added, v)
		}
	}
	for _, v := range current {
		if !want[v] {
			removed = append(removed, v)
		}
	}
	return added, removed
}

// sameFQDNModifier requires replacement when `name` changes, unless the new
// spelling names the same record (`www` vs `www.example.com`).
type sameFQDNModifier struct{}

func (sameFQDNModifier) Description(context.Context) string {
	return "Requires replacement unless the new name qualifies to the same fully qualified name."
}

func (m sameFQDNModifier) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (sameFQDNModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || req.PlanValue.IsUnknown() || req.PlanValue.Equal(req.StateValue) {
		return
	}
	var zoneName, fqdn types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("zone_name"), &zoneName)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("fqdn"), &fqdn)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !zoneName.IsNull() && zoneName.ValueString() != "" &&
		qualifyName(req.PlanValue.ValueString(), zoneName.ValueString()) == fqdn.ValueString() {
		return
	}
	resp.RequiresReplace = true
}
