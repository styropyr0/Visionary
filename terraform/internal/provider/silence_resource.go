package provider

import (
	"context"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/objectvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type alertSilenceResource struct{ base }

func newAlertSilenceResource() resource.Resource { return &alertSilenceResource{} }

var _ resource.ResourceWithImportState = (*alertSilenceResource)(nil)

type silenceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Enabled    types.Bool   `tfsdk:"enabled"`
	RuleIDs    types.List   `tfsdk:"rule_ids"`
	Severities types.List   `tfsdk:"severities"`
	Labels     types.Map    `tfsdk:"labels"`
	Series     types.String `tfsdk:"series"`
	Once       *onceModel   `tfsdk:"once"`
	Weekly     *weeklyModel `tfsdk:"weekly"`
	State      types.String `tfsdk:"state"`
}

type onceModel struct {
	StartsAt types.String `tfsdk:"starts_at"`
	EndsAt   types.String `tfsdk:"ends_at"`
}

type weeklyModel struct {
	Days types.List   `tfsdk:"days"`
	From types.String `tfsdk:"from"`
	To   types.String `tfsdk:"to"`
	TZ   types.String `tfsdk:"tz"`
}

type apiSilence struct {
	ID       int64       `json:"id,omitempty"`
	Name     string      `json:"name"`
	Matchers apiMatchers `json:"matchers"`
	Schedule apiSchedule `json:"schedule"`
	Enabled  *bool       `json:"enabled"`
	State    string      `json:"state,omitempty"`
}

type apiMatchers struct {
	RuleIDs    []int64           `json:"rule_ids,omitempty"`
	Severities []string          `json:"severities,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Series     string            `json:"series,omitempty"`
}

type apiSchedule struct {
	Kind     string  `json:"kind"`
	StartsAt string  `json:"starts_at,omitempty"`
	EndsAt   string  `json:"ends_at,omitempty"`
	Days     []int64 `json:"days,omitempty"`
	From     string  `json:"from,omitempty"`
	To       string  `json:"to,omitempty"`
	TZ       string  `json:"tz,omitempty"`
}

func (r *alertSilenceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_silence"
}

func (r *alertSilenceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	clock := []validator.String{stringvalidator.RegexMatches(clockRe, "must be HH:MM")}
	resp.Schema = schema.Schema{
		Description: "A silence holds back notifications for matching alerts: a maintenance window (once) or quiet hours (weekly). " +
			"Matchers are ANDed; none matches every alert.",
		Attributes: map[string]schema.Attribute{
			"id":       idAttribute(),
			"name":     schema.StringAttribute{Description: "Name.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 120)}},
			"enabled":  schema.BoolAttribute{Description: "Default true.", Optional: true, Computed: true, Default: booldefault.StaticBool(true)},
			"rule_ids": schema.ListAttribute{Description: "Only these rules (ids).", Optional: true, ElementType: types.StringType, Validators: []validator.List{listvalidator.SizeAtLeast(1)}},
			"severities": schema.ListAttribute{
				Description: "Only these severities.", Optional: true, ElementType: types.StringType,
				Validators: []validator.List{listvalidator.SizeAtLeast(1), listvalidator.ValueStringsAre(stringvalidator.OneOf("info", "warning", "critical"))},
			},
			"labels": schema.MapAttribute{Description: "Only rules with these labels.", Optional: true, ElementType: types.StringType},
			"series": schema.StringAttribute{Description: "Only this series (instance, check, host or container); * is a wildcard.", Optional: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 200)}},
			"once": schema.SingleNestedAttribute{
				Description: "A window between two times.",
				Optional:    true,
				Validators:  []validator.Object{objectvalidator.ExactlyOneOf(path.MatchRoot("once"), path.MatchRoot("weekly"))},
				Attributes: map[string]schema.Attribute{
					"starts_at": schema.StringAttribute{Description: "RFC 3339 start.", Required: true, Validators: []validator.String{rfc3339Validator{}}},
					"ends_at":   schema.StringAttribute{Description: "RFC 3339 end, at most 90 days later.", Required: true, Validators: []validator.String{rfc3339Validator{}}},
				},
			},
			"weekly": schema.SingleNestedAttribute{
				Description: "Quiet hours on some days of the week; a to before from spans midnight.",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"days": schema.ListAttribute{Description: "Days, 0 (Sunday) to 6.", Required: true, ElementType: types.Int64Type, Validators: []validator.List{listvalidator.SizeBetween(1, 7)}},
					"from": schema.StringAttribute{Description: "HH:MM.", Required: true, Validators: clock},
					"to":   schema.StringAttribute{Description: "HH:MM.", Required: true, Validators: clock},
					"tz":   schema.StringAttribute{Description: "IANA time zone, such as Europe/Berlin (default UTC).", Optional: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
				},
			},
			"state": schema.StringAttribute{Description: "active, scheduled, ended or disabled, when last read.", Computed: true},
		},
	}
}

func (m *silenceModel) input(ctx context.Context, diags *diag.Diagnostics) apiSilence {
	enabled := m.Enabled.ValueBool()
	in := apiSilence{Name: m.Name.ValueString(), Enabled: &enabled, Matchers: apiMatchers{
		RuleIDs: idsOf(ctx, m.RuleIDs, diags), Severities: stringsOf(ctx, m.Severities, diags),
		Labels: mapOf(ctx, m.Labels, diags), Series: m.Series.ValueString(),
	}}
	if m.Once != nil {
		in.Schedule = apiSchedule{Kind: "once", StartsAt: m.Once.StartsAt.ValueString(), EndsAt: m.Once.EndsAt.ValueString()}
	}
	if w := m.Weekly; w != nil {
		in.Schedule = apiSchedule{Kind: "weekly", From: w.From.ValueString(), To: w.To.ValueString(), TZ: w.TZ.ValueString()}
		diags.Append(w.Days.ElementsAs(ctx, &in.Schedule.Days, false)...)
	}
	return in
}

func sameTime(server string, ref types.String) types.String {
	a, err1 := time.Parse(time.RFC3339, server)
	b, err2 := time.Parse(time.RFC3339, ref.ValueString())
	if err1 == nil && err2 == nil && a.Equal(b) {
		return ref
	}
	return types.StringValue(server)
}

func (m *silenceModel) fill(s apiSilence) {
	m.ID, m.Name = idString(s.ID), types.StringValue(s.Name)
	m.Enabled = types.BoolValue(s.Enabled == nil || *s.Enabled)
	m.RuleIDs, m.Severities = optIDList(s.Matchers.RuleIDs, m.RuleIDs), optStringList(s.Matchers.Severities, m.Severities)
	m.Labels, m.Series = optStringMap(s.Matchers.Labels, m.Labels), optString(s.Matchers.Series, m.Series)
	m.State = types.StringValue(s.State)
	sc := s.Schedule
	switch sc.Kind {
	case "once":
		prev := m.Once
		if prev == nil {
			prev = &onceModel{}
		}
		m.Once, m.Weekly = &onceModel{StartsAt: sameTime(sc.StartsAt, prev.StartsAt), EndsAt: sameTime(sc.EndsAt, prev.EndsAt)}, nil
	case "weekly":
		tz := types.StringNull()
		if m.Weekly != nil {
			tz = m.Weekly.TZ
		}
		days := make([]attr.Value, len(sc.Days))
		for i, d := range sc.Days {
			days[i] = types.Int64Value(d)
		}
		m.Weekly = &weeklyModel{Days: types.ListValueMust(types.Int64Type, days), From: types.StringValue(sc.From), To: types.StringValue(sc.To), TZ: optString(sc.TZ, tz)}
		m.Once = nil
	}
}

func (r *alertSilenceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan silenceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.input(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiSilence
	if err := r.c.do(ctx, http.MethodPost, "/api/alerts/silences", in, &out); err != nil {
		apiDiag(&resp.Diagnostics, "create silence", err)
		return
	}
	plan.fill(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *alertSilenceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state silenceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiSilence
	err := r.c.do(ctx, http.MethodGet, "/api/alerts/silences/"+state.ID.ValueString(), nil, &out)
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiag(&resp.Diagnostics, "read silence", err)
		return
	}
	state.fill(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *alertSilenceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior silenceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.input(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiSilence
	if err := r.c.do(ctx, http.MethodPut, "/api/alerts/silences/"+prior.ID.ValueString(), in, &out); err != nil {
		apiDiag(&resp.Diagnostics, "update silence", err)
		return
	}
	plan.fill(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *alertSilenceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state silenceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.c.do(ctx, http.MethodDelete, "/api/alerts/silences/"+state.ID.ValueString(), nil, nil)
	if err != nil && !isNotFound(err) {
		apiDiag(&resp.Diagnostics, "delete silence", err)
	}
}
