package provider

import (
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// uptimeMask is what the API shows instead of a secret header value or a
// browser secret.
const uptimeMask = "********"

var browserActions = []string{"goto", "click", "type", "press", "select", "wait_selector", "wait_text", "wait_navigation",
	"assert_text", "assert_no_text", "assert_url", "assert_count", "wait", "script"}

type uptimeCheckResource struct{ base }

func newUptimeCheckResource() resource.Resource { return &uptimeCheckResource{} }

var (
	_ resource.ResourceWithImportState    = (*uptimeCheckResource)(nil)
	_ resource.ResourceWithValidateConfig = (*uptimeCheckResource)(nil)
)

type checkModel struct {
	ID              types.String    `tfsdk:"id"`
	Name            types.String    `tfsdk:"name"`
	Kind            types.String    `tfsdk:"kind"`
	URL             types.String    `tfsdk:"url"`
	Method          types.String    `tfsdk:"method"`
	ExpectStatus    types.String    `tfsdk:"expect_status"`
	BodyContains    types.String    `tfsdk:"body_contains"`
	Headers         types.Map       `tfsdk:"headers"`
	IntervalS       types.Int64     `tfsdk:"interval_s"`
	TimeoutMs       types.Int64     `tfsdk:"timeout_ms"`
	DegradedMs      types.Int64     `tfsdk:"degraded_ms"`
	FollowRedirects types.Bool      `tfsdk:"follow_redirects"`
	Enabled         types.Bool      `tfsdk:"enabled"`
	OnStatusPage    types.Bool      `tfsdk:"on_status_page"`
	StatusShowURL   types.Bool      `tfsdk:"status_show_url"`
	Steps           []stepModel     `tfsdk:"steps"`
	Secrets         types.Map       `tfsdk:"secrets"`
	FinalScreenshot types.Bool      `tfsdk:"final_screenshot"`
	API             *apiModel       `tfsdk:"api"`
	TCP             *tcpModel       `tfsdk:"tcp"`
	DNS             *dnsModel       `tfsdk:"dns"`
	Ping            *pingModel      `tfsdk:"ping"`
	SSL             *sslModel       `tfsdk:"ssl"`
	Heartbeat       *heartbeatModel `tfsdk:"heartbeat"`
	HeartbeatPath   types.String    `tfsdk:"heartbeat_path"`
}

type stepModel struct {
	Action   types.String `tfsdk:"action"`
	Selector types.String `tfsdk:"selector"`
	Text     types.String `tfsdk:"text"`
	Value    types.String `tfsdk:"value"`
	Count    types.Int64  `tfsdk:"count"`
	Op       types.String `tfsdk:"op"`
	Ms       types.Int64  `tfsdk:"ms"`
	Label    types.String `tfsdk:"label"`
}

type apiCheck struct {
	ID              int64             `json:"id,omitempty"`
	Name            string            `json:"name"`
	Kind            string            `json:"kind"`
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	ExpectStatus    string            `json:"expect_status"`
	BodyContains    string            `json:"body_contains"`
	Headers         map[string]string `json:"headers"`
	IntervalS       int64             `json:"interval_s"`
	TimeoutMs       int64             `json:"timeout_ms"`
	DegradedMs      int64             `json:"degraded_ms"`
	FollowRedirects bool              `json:"follow_redirects"`
	Enabled         bool              `json:"enabled"`
	OnStatusPage    bool              `json:"on_status_page"`
	StatusShowURL   bool              `json:"status_show_url"`
	Steps           []apiStepB        `json:"steps,omitempty"`
	Secrets         map[string]string `json:"secrets,omitempty"`
	FinalScreenshot bool              `json:"final_screenshot,omitempty"`
	API             *apiAPI           `json:"api,omitempty"`
	TCP             *apiTCP           `json:"tcp,omitempty"`
	DNS             *apiDNS           `json:"dns,omitempty"`
	Ping            *apiPing          `json:"ping,omitempty"`
	SSL             *apiSSL           `json:"ssl,omitempty"`
	Heartbeat       *apiHeartbeat     `json:"heartbeat,omitempty"`
}

type apiStepB struct {
	Action   string `json:"action"`
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	Value    string `json:"value,omitempty"`
	Count    int64  `json:"count,omitempty"`
	Op       string `json:"op,omitempty"`
	Ms       int64  `json:"ms,omitempty"`
	Label    string `json:"label,omitempty"`
}

func (r *uptimeCheckResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_uptime_check"
}

func (r *uptimeCheckResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	step := map[string]schema.Attribute{
		"action":   schema.StringAttribute{Description: "goto, click, type, press, select, wait_selector, wait_text, wait_navigation, assert_text, assert_no_text, assert_url, assert_count, wait or script.", Required: true, Validators: []validator.String{stringvalidator.OneOf(browserActions...)}},
		"selector": optStr("CSS selector."),
		"text":     optStr("Text to find."),
		"value":    optStr("URL, text to type, key, option, script; {{secret.NAME}} uses a secret."),
		"count":    schema.Int64Attribute{Description: "Expected count (assert_count).", Optional: true},
		"op":       optStr("Comparison for assert_count: eq, gte or lte."),
		"ms":       schema.Int64Attribute{Description: "Milliseconds (wait).", Optional: true},
		"label":    optStr("Name shown in results."),
	}
	resp.Schema = schema.Schema{
		Description: "An uptime check: an HTTP probe, a browser check that plays steps in a headless browser, a multi-step API check, " +
			"a TCP, DNS, ping or certificate check, or a heartbeat that a job pings. " +
			"Like a check made in the dashboard, a new check is alerted on by the rule for every check (made if there is none).",
		Attributes: map[string]schema.Attribute{
			"id":   idAttribute(),
			"name": schema.StringAttribute{Description: "Name.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 120)}},
			"kind": schema.StringAttribute{
				Description: "http (default), browser, api, tcp, dns, ping, ssl or heartbeat; each kind but http and browser has a block of the same name.",
				Optional:    true, Computed: true, Default: stringdefault.StaticString("http"),
				Validators: []validator.String{stringvalidator.OneOf(checkKinds...)},
			},
			"url": schema.StringAttribute{
				Description: "Address probed (http checks). For other kinds it is derived: the first step's or request's URL, or tcp://, dns://, ping:// or ssl:// with the target.",
				Optional:    true, Computed: true, Validators: []validator.String{httpURLValidator{}},
				PlanModifiers: []planmodifier.String{keepIf("kind", "steps", "api", "tcp", "dns", "ping", "ssl")},
			},
			"method": schema.StringAttribute{
				Description: "HTTP method (default GET).", Optional: true, Computed: true, PlanModifiers: []planmodifier.String{keepIf("kind")},
				Validators: []validator.String{stringvalidator.OneOf("GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS")},
			},
			"expect_status": schema.StringAttribute{Description: "Accepted statuses: 200, 200-299, 2xx, comma separated (default 200-299).", Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{keepIf("kind")}},
			"body_contains": schema.StringAttribute{Description: "Text the body must contain.", Optional: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 512)}},
			"headers":       schema.MapAttribute{Description: "Request headers.", Optional: true, Sensitive: true, ElementType: types.StringType},
			"interval_s": schema.Int64Attribute{
				Description: "Seconds between runs, 30 to 3600 (default 60; 300 for browser checks).", Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Int64{keepIf("kind")},
				Validators:    []validator.Int64{int64validator.Between(30, 3600)},
			},
			"timeout_ms": schema.Int64Attribute{
				Description: "Timeout in ms (default 10000; 30000 for browser and api checks; for ping checks each reply's wait, default 2000).", Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Int64{keepIf("kind")},
				Validators:    []validator.Int64{int64validator.Between(100, 120000)},
			},
			"degraded_ms":      schema.Int64Attribute{Description: "Slower than this counts as degraded.", Optional: true, Validators: []validator.Int64{int64validator.Between(1, 120000)}},
			"follow_redirects": schema.BoolAttribute{Description: "Default true.", Optional: true, Computed: true, Default: booldefault.StaticBool(true)},
			"enabled":          schema.BoolAttribute{Description: "Default true.", Optional: true, Computed: true, Default: booldefault.StaticBool(true)},
			"on_status_page":   schema.BoolAttribute{Description: "Show on the public status page.", Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
			"status_show_url":  schema.BoolAttribute{Description: "Show the URL on the status page.", Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
			"steps": schema.ListNestedAttribute{
				Description:  "Browser check steps; the first must be goto.",
				Optional:     true,
				Validators:   []validator.List{listvalidator.SizeBetween(1, 50)},
				NestedObject: schema.NestedAttributeObject{Attributes: step},
			},
			"secrets":          schema.MapAttribute{Description: "Browser and api check secrets, used as {{secret.NAME}}.", Optional: true, Sensitive: true, ElementType: types.StringType},
			"final_screenshot": schema.BoolAttribute{Description: "Keep a screenshot of every browser run, not only failed ones.", Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
		},
	}
	for k, v := range kindAttributes() {
		resp.Schema.Attributes[k] = v
	}
}

func (r *uptimeCheckResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var kind types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("kind"), &kind)...)
	if kind.IsUnknown() {
		return
	}
	set := func(name string) bool {
		var v attr.Value
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(name), &v)...)
		return v != nil && !v.IsNull()
	}
	k := kind.ValueString()
	if k == "" {
		k = "http"
	}
	for n, why := range kindProblems(k, set) {
		resp.Diagnostics.AddAttributeError(path.Root(n), "Invalid for "+k+" checks", why)
	}
	var hb *heartbeatModel
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("heartbeat"), &hb)...)
	if hb != nil && !hb.PeriodS.IsUnknown() && !hb.Cron.IsUnknown() && hb.PeriodS.IsNull() == hb.Cron.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("heartbeat"), "Period or cron", "Give heartbeat.period_s or heartbeat.cron, not both.")
	}
	var onPage, showURL types.Bool
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("on_status_page"), &onPage)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("status_show_url"), &showURL)...)
	if showURL.ValueBool() && !onPage.ValueBool() && !onPage.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("status_show_url"), "Not on the status page", "status_show_url needs on_status_page = true.")
	}
}

func (m *checkModel) input(ctx context.Context, diags *diag.Diagnostics) apiCheck {
	in := apiCheck{
		Name: m.Name.ValueString(), Kind: m.Kind.ValueString(), URL: m.URL.ValueString(), Method: m.Method.ValueString(),
		ExpectStatus: m.ExpectStatus.ValueString(), BodyContains: m.BodyContains.ValueString(), Headers: mapOf(ctx, m.Headers, diags),
		IntervalS: m.IntervalS.ValueInt64(), TimeoutMs: m.TimeoutMs.ValueInt64(), DegradedMs: m.DegradedMs.ValueInt64(),
		FollowRedirects: m.FollowRedirects.ValueBool(), Enabled: m.Enabled.ValueBool(), OnStatusPage: m.OnStatusPage.ValueBool(),
		StatusShowURL: m.StatusShowURL.ValueBool(), Secrets: mapOf(ctx, m.Secrets, diags), FinalScreenshot: m.FinalScreenshot.ValueBool(),
	}
	if in.Headers == nil {
		in.Headers = map[string]string{}
	}
	for _, s := range m.Steps {
		in.Steps = append(in.Steps, apiStepB{Action: s.Action.ValueString(), Selector: s.Selector.ValueString(), Text: s.Text.ValueString(),
			Value: s.Value.ValueString(), Count: s.Count.ValueInt64(), Op: s.Op.ValueString(), Ms: s.Ms.ValueInt64(), Label: s.Label.ValueString()})
	}
	m.kindInput(ctx, &in, diags)
	return in
}

// unmask puts back the values the API hides, from ref.
func unmask(server map[string]string, ref types.Map) types.Map {
	known := map[string]string{}
	if !ref.IsNull() && !ref.IsUnknown() {
		for k, v := range ref.Elements() {
			if s, ok := v.(types.String); ok {
				known[k] = s.ValueString()
			}
		}
	}
	out := make(map[string]string, len(server))
	for k, v := range server {
		if old, ok := known[k]; ok && v == uptimeMask {
			v = old
		}
		out[k] = v
	}
	return optStringMap(out, ref)
}

func (m *checkModel) fill(c apiCheck) {
	m.ID, m.Name, m.Kind, m.URL = idString(c.ID), types.StringValue(c.Name), types.StringValue(c.Kind), types.StringValue(c.URL)
	m.Method, m.ExpectStatus, m.BodyContains = types.StringValue(c.Method), types.StringValue(c.ExpectStatus), optString(c.BodyContains, m.BodyContains)
	m.Headers, m.Secrets = unmask(c.Headers, m.Headers), unmask(c.Secrets, m.Secrets)
	m.IntervalS, m.TimeoutMs, m.DegradedMs = types.Int64Value(c.IntervalS), types.Int64Value(c.TimeoutMs), optInt(c.DegradedMs, m.DegradedMs)
	m.FollowRedirects, m.Enabled = types.BoolValue(c.FollowRedirects), types.BoolValue(c.Enabled)
	m.OnStatusPage, m.StatusShowURL, m.FinalScreenshot = types.BoolValue(c.OnStatusPage), types.BoolValue(c.StatusShowURL), types.BoolValue(c.FinalScreenshot)
	m.kindFill(c)
	if c.Kind != "heartbeat" {
		m.HeartbeatPath = types.StringNull()
	}
	if len(c.Steps) == 0 && m.Steps == nil {
		m.Steps = nil
		return
	}
	steps := make([]stepModel, len(c.Steps))
	for i, s := range c.Steps {
		ref := stepModel{Selector: types.StringNull(), Text: types.StringNull(), Value: types.StringNull(), Count: types.Int64Null(),
			Op: types.StringNull(), Ms: types.Int64Null(), Label: types.StringNull()}
		if i < len(m.Steps) {
			ref = m.Steps[i]
		}
		steps[i] = stepModel{Action: types.StringValue(s.Action), Selector: optString(s.Selector, ref.Selector), Text: optString(s.Text, ref.Text),
			Value: optString(s.Value, ref.Value), Count: optInt(s.Count, ref.Count), Op: optString(s.Op, ref.Op), Ms: optInt(s.Ms, ref.Ms),
			Label: optString(s.Label, ref.Label)}
	}
	m.Steps = steps
}

type checkAnswer struct {
	Check      apiCheck `json:"check"`
	AlertError string   `json:"alert_error"`
}

func (r *uptimeCheckResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan checkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.input(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var out checkAnswer
	if err := r.c.do(ctx, http.MethodPost, "/api/uptime/checks", in, &out); err != nil {
		apiDiag(&resp.Diagnostics, "create uptime check", err)
		return
	}
	if out.AlertError != "" {
		resp.Diagnostics.AddWarning("Uptime check alert", out.AlertError)
	}
	plan.fill(out.Check)
	r.readHeartbeatPath(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *uptimeCheckResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state checkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out checkAnswer
	err := r.c.do(ctx, http.MethodGet, "/api/uptime/checks/"+state.ID.ValueString(), nil, &out)
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiag(&resp.Diagnostics, "read uptime check", err)
		return
	}
	state.fill(out.Check)
	r.readHeartbeatPath(ctx, &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *uptimeCheckResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior checkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.input(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var out checkAnswer
	if err := r.c.do(ctx, http.MethodPut, "/api/uptime/checks/"+prior.ID.ValueString(), in, &out); err != nil {
		apiDiag(&resp.Diagnostics, "update uptime check", err)
		return
	}
	plan.fill(out.Check)
	r.readHeartbeatPath(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *uptimeCheckResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state checkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.c.do(ctx, http.MethodDelete, "/api/uptime/checks/"+state.ID.ValueString(), nil, nil)
	if err != nil && !isNotFound(err) {
		apiDiag(&resp.Diagnostics, "delete uptime check", err)
	}
}
