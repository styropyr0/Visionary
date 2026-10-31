package provider

import (
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// uptimeMask is what the API shows instead of a secret header value or a
// browser secret.
const uptimeMask = "********"

var browserActions = []string{"goto", "click", "type", "press", "select", "wait_selector", "wait_text", "wait_navigation",
	"assert_text", "assert_no_text", "assert_url", "assert_count", "wait", "script", "extract", "hover", "drag", "upload", "frame",
	"set_cookie", "set_storage", "grant_permission"}

type uptimeCheckResource struct{ base }

func newUptimeCheckResource() resource.Resource { return &uptimeCheckResource{} }

var (
	_ resource.ResourceWithImportState    = (*uptimeCheckResource)(nil)
	_ resource.ResourceWithValidateConfig = (*uptimeCheckResource)(nil)
)

type checkModel struct {
	ID              types.String  `tfsdk:"id"`
	Name            types.String  `tfsdk:"name"`
	Kind            types.String  `tfsdk:"kind"`
	URL             types.String  `tfsdk:"url"`
	Method          types.String  `tfsdk:"method"`
	ExpectStatus    types.String  `tfsdk:"expect_status"`
	BodyContains    types.String  `tfsdk:"body_contains"`
	Headers         types.Map     `tfsdk:"headers"`
	IntervalS       types.Int64   `tfsdk:"interval_s"`
	TimeoutMs       types.Int64   `tfsdk:"timeout_ms"`
	DegradedMs      types.Int64   `tfsdk:"degraded_ms"`
	FollowRedirects types.Bool    `tfsdk:"follow_redirects"`
	Enabled         types.Bool    `tfsdk:"enabled"`
	OnStatusPage    types.Bool    `tfsdk:"on_status_page"`
	StatusShowURL   types.Bool    `tfsdk:"status_show_url"`
	Steps           []stepModel   `tfsdk:"steps"`
	Secrets         types.Map     `tfsdk:"secrets"`
	FinalScreenshot types.Bool    `tfsdk:"final_screenshot"`
	TraceOrigins    types.List    `tfsdk:"trace_origins"`
	TraceHeaders    types.Bool    `tfsdk:"trace_headers"`
	MaxLCPMs        types.Int64   `tfsdk:"max_lcp_ms"`
	MaxCLS          types.Float64 `tfsdk:"max_cls"`
	MaxTTFBMs       types.Int64   `tfsdk:"max_ttfb_ms"`
	MaxDurationMs   types.Int64   `tfsdk:"max_duration_ms"`
	WarnLCPMs       types.Int64   `tfsdk:"warn_lcp_ms"`
	WarnCLS         types.Float64 `tfsdk:"warn_cls"`
	WarnTTFBMs      types.Int64   `tfsdk:"warn_ttfb_ms"`
	Device          types.String  `tfsdk:"device"`
	ViewportW       types.Int64   `tfsdk:"viewport_w"`
	ViewportH       types.Int64   `tfsdk:"viewport_h"`
	StepScreenshots types.Bool    `tfsdk:"step_screenshots"`
	Files           []fileModel   `tfsdk:"files"`
	Locations       types.List    `tfsdk:"locations"`
	LocationRule    types.String  `tfsdk:"location_rule"`
	StatusShowLocs  types.Bool    `tfsdk:"status_show_locations"`
	Tags            types.Set     `tfsdk:"tags"`
	Playwright      *pwModel      `tfsdk:"playwright"`
}

type fileModel struct {
	Name    types.String `tfsdk:"name"`
	Type    types.String `tfsdk:"type"`
	Content types.String `tfsdk:"content_base64"`
}

type apiFile struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	Data string `json:"data,omitempty"`
}

type pwModel struct {
	Project types.String `tfsdk:"project"`
	Title   types.String `tfsdk:"title"`
}

type stepModel struct {
	Action     types.String  `tfsdk:"action"`
	Selector   types.String  `tfsdk:"selector"`
	Text       types.String  `tfsdk:"text"`
	Value      types.String  `tfsdk:"value"`
	Count      types.Int64   `tfsdk:"count"`
	Op         types.String  `tfsdk:"op"`
	Ms         types.Int64   `tfsdk:"ms"`
	Label      types.String  `tfsdk:"label"`
	MaxMs      types.Int64   `tfsdk:"max_ms"`
	Name       types.String  `tfsdk:"name"`
	Attr       types.String  `tfsdk:"attr"`
	Regex      types.String  `tfsdk:"regex"`
	Target     types.String  `tfsdk:"target"`
	Scope      types.String  `tfsdk:"scope"`
	Permission types.String  `tfsdk:"permission"`
	Lat        types.Float64 `tfsdk:"lat"`
	Lon        types.Float64 `tfsdk:"lon"`
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
	TraceOrigins    []string          `json:"trace_origins,omitempty"`
	NoTrace         bool              `json:"no_trace,omitempty"`
	MaxLCPMs        int64             `json:"max_lcp_ms,omitempty"`
	MaxCLS          float64           `json:"max_cls,omitempty"`
	MaxTTFBMs       int64             `json:"max_ttfb_ms,omitempty"`
	MaxDurationMs   int64             `json:"max_duration_ms,omitempty"`
	WarnLCPMs       int64             `json:"warn_lcp_ms,omitempty"`
	WarnCLS         float64           `json:"warn_cls,omitempty"`
	WarnTTFBMs      int64             `json:"warn_ttfb_ms,omitempty"`
	Device          string            `json:"device,omitempty"`
	ViewportW       int64             `json:"viewport_w,omitempty"`
	ViewportH       int64             `json:"viewport_h,omitempty"`
	StepScreenshots bool              `json:"step_screenshots,omitempty"`
	Files           []apiFile         `json:"files,omitempty"`
	Locations       []string          `json:"locations"`
	LocationRule    string            `json:"location_rule"`
	StatusShowLocs  bool              `json:"status_show_locations"`
	Tags            []string          `json:"tags"`
	Playwright      *apiPlaywright    `json:"playwright,omitempty"`
}

type apiPlaywright struct {
	Project string `json:"project"`
	Title   string `json:"title"`
}

type apiStepB struct {
	Action     string  `json:"action"`
	Selector   string  `json:"selector,omitempty"`
	Text       string  `json:"text,omitempty"`
	Value      string  `json:"value,omitempty"`
	Count      int64   `json:"count,omitempty"`
	Op         string  `json:"op,omitempty"`
	Ms         int64   `json:"ms,omitempty"`
	Label      string  `json:"label,omitempty"`
	MaxMs      int64   `json:"max_ms,omitempty"`
	Name       string  `json:"name,omitempty"`
	Attr       string  `json:"attr,omitempty"`
	Regex      string  `json:"regex,omitempty"`
	Target     string  `json:"target,omitempty"`
	Scope      string  `json:"scope,omitempty"`
	Permission string  `json:"permission,omitempty"`
	Lat        float64 `json:"lat,omitempty"`
	Lon        float64 `json:"lon,omitempty"`
}

func (r *uptimeCheckResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_uptime_check"
}

func (r *uptimeCheckResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	step := map[string]schema.Attribute{
		"action": schema.StringAttribute{Description: "goto, click, type, press, select, wait_selector, wait_text, wait_navigation, assert_text, assert_no_text, assert_url, assert_count, wait, script, " +
			"extract, hover, drag, upload, frame, set_cookie, set_storage or grant_permission.", Required: true, Validators: []validator.String{stringvalidator.OneOf(browserActions...)}},
		"name":       optStr("The variable extract sets (used later as {{var.NAME}}), the cookie of set_cookie, the key of set_storage or the file of upload."),
		"attr":       optStr("What extract reads: text (default), value or an attribute name."),
		"regex":      optStr("For extract: keep the first group (or the whole match) of this regular expression."),
		"target":     optStr("CSS selector drag drops on."),
		"scope":      optStr("set_cookie: the cookie's URL or domain (default the current page); set_storage: local (default) or session; grant_permission: the origin (default the current page's)."),
		"permission": optStr("grant_permission: geolocation, notifications or clipboard."),
		"lat":        schema.Float64Attribute{Description: "Latitude for grant_permission geolocation.", Optional: true},
		"lon":        schema.Float64Attribute{Description: "Longitude for grant_permission geolocation.", Optional: true},
		"selector":   optStr("CSS selector."),
		"text":       optStr("Text to find."),
		"value":      optStr("URL, text to type, key, option, script; {{secret.NAME}} uses a secret."),
		"count":      schema.Int64Attribute{Description: "Expected count (assert_count).", Optional: true},
		"op":         optStr("Comparison for assert_count: eq, gte or lte."),
		"ms":         schema.Int64Attribute{Description: "Milliseconds (wait).", Optional: true},
		"label":      optStr("Name shown in results."),
		"max_ms":     schema.Int64Attribute{Description: "Fail the step when it takes longer than this.", Optional: true, Validators: []validator.Int64{int64validator.Between(1, 120000)}},
	}
	limit := func(what string) schema.Int64Attribute {
		return schema.Int64Attribute{Description: what, Optional: true, Validators: []validator.Int64{int64validator.Between(1, 120000)}}
	}
	resp.Schema = schema.Schema{
		Description: "An uptime check: an HTTP probe, a browser check that plays steps in a headless browser, or a Playwright test " +
			"your own runs report (declared here so it exists before its first run). " +
			"Like a check made in the dashboard, a new check is alerted on by the rule for every check (made if there is none).",
		Attributes: map[string]schema.Attribute{
			"id":   idAttribute(),
			"name": schema.StringAttribute{Description: "Name.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 120)}},
			"kind": schema.StringAttribute{
				Description: "http (default), browser or playwright.", Optional: true, Computed: true, Default: stringdefault.StaticString("http"),
				Validators: []validator.String{stringvalidator.OneOf("http", "browser", "playwright")},
			},
			"url": schema.StringAttribute{
				Description: "Address probed (http checks). For browser checks it is the first step's URL.",
				Optional:    true, Computed: true, Validators: []validator.String{httpURLValidator{}},
				PlanModifiers: []planmodifier.String{keepIf("kind", "steps")},
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
				Description: "Seconds between runs, 30 to 3600 (default 60; 300 for browser checks). For playwright checks, how often a run is expected, 60 to 604800 (default 3600); without one for longer the check is stale.",
				Optional:    true, Computed: true,
				PlanModifiers: []planmodifier.Int64{keepIf("kind")},
				Validators:    []validator.Int64{int64validator.Between(30, 604800)},
			},
			"timeout_ms": schema.Int64Attribute{
				Description: "Timeout in ms (default 10000; 30000 for browser checks).", Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Int64{keepIf("kind")},
				Validators:    []validator.Int64{int64validator.Between(1000, 120000)},
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
			"secrets":          schema.MapAttribute{Description: "Browser check secrets, used in steps as {{secret.NAME}}.", Optional: true, Sensitive: true, ElementType: types.StringType},
			"final_screenshot": schema.BoolAttribute{Description: "Keep a screenshot of every browser run, not only failed ones.", Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
			"trace_headers": schema.BoolAttribute{
				Description: "Send a W3C traceparent with each browser run's trace id, so the run links to its backend trace (default true).",
				Optional:    true, Computed: true, Default: booldefault.StaticBool(true),
			},
			"max_lcp_ms":      limit("A browser run whose Largest Contentful Paint is slower than this fails."),
			"max_cls":         schema.Float64Attribute{Description: "A browser run whose Cumulative Layout Shift is above this fails.", Optional: true},
			"max_ttfb_ms":     limit("A browser run whose time to first byte is slower than this fails."),
			"max_duration_ms": limit("A browser run that takes longer than this in all fails."),
			"warn_lcp_ms":     limit("A browser run whose LCP is slower than this counts as degraded."),
			"warn_cls":        schema.Float64Attribute{Description: "A browser run whose CLS is above this counts as degraded.", Optional: true},
			"warn_ttfb_ms":    limit("A browser run whose TTFB is slower than this counts as degraded."),
			"device": schema.StringAttribute{
				Description: "Screen a browser check runs at: desktop (1280×800, default), laptop (1440×900), tablet (768×1024), phone (390×844, mobile user agent, touch, pixel ratio 3) or custom (viewport_w × viewport_h).",
				Optional:    true, Computed: true, Default: stringdefault.StaticString("desktop"),
				Validators: []validator.String{stringvalidator.OneOf("desktop", "laptop", "tablet", "phone", "custom")},
			},
			"files": schema.ListNestedAttribute{
				Description: "Small files upload steps send, 256 KB each and 512 KB in all.",
				Optional:    true,
				Validators:  []validator.List{listvalidator.SizeAtMost(5)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":           schema.StringAttribute{Description: "File name an upload step refers to.", Required: true},
					"type":           optStr("MIME type."),
					"content_base64": schema.StringAttribute{Description: "The file, base64 encoded, e.g. filebase64(\"avatar.png\").", Required: true},
				}},
			},
			"step_screenshots": schema.BoolAttribute{Description: "Keep a small screenshot after every step of a browser run (the latest 24 runs keep theirs).", Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
			"viewport_w":       schema.Int64Attribute{Description: "Width of a custom device, 320 to 3840.", Optional: true, Validators: []validator.Int64{int64validator.Between(320, 3840)}},
			"viewport_h":       schema.Int64Attribute{Description: "Height of a custom device, 320 to 2160.", Optional: true, Validators: []validator.Int64{int64validator.Between(320, 2160)}},
			"trace_origins": schema.ListAttribute{
				Description: "Origins that get the traceparent header, such as https://api.example.com or https://*.example.com. Default: the first page's origin.",
				Optional:    true, ElementType: types.StringType, Validators: []validator.List{listvalidator.SizeAtMost(10)},
			},
			"tags": schema.SetAttribute{
				Description: "Tags that group checks (smoke, checkout); a run from CI picks checks by tag. Lower case letters, digits and _ . : -.",
				Optional:    true, ElementType: types.StringType,
				Validators: []validator.Set{setvalidator.SizeAtMost(20), setvalidator.ValueStringsAre(stringvalidator.RegexMatches(tagRe, "must be lower case letters, digits and _ . : -, up to 40"))},
			},
			"playwright": schema.SingleNestedAttribute{
				Description: "The Playwright test a playwright check follows, as visionary-playwright-reporter reports it.",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"project": schema.StringAttribute{Description: "Playwright project name (empty without projects).", Optional: true, Computed: true, Default: stringdefault.StaticString("")},
					"title":   schema.StringAttribute{Description: "Title path: file › describe › test, e.g. checkout.spec.ts › pays by card.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 1000)}},
				},
			},
			"locations": schema.ListAttribute{
				Description: `Where the check runs from: "server" (the Visionary server) and names of visionary_uptime_location (default ["server"]).`,
				Optional:    true, Computed: true, ElementType: types.StringType,
				Default:    listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{types.StringValue("server")})),
				Validators: []validator.List{listvalidator.SizeBetween(1, 10)},
			},
			"location_rule": schema.StringAttribute{
				Description: "With several locations, the check is down when any, a majority (default) or all of them fail.", Optional: true, Computed: true,
				Default: stringdefault.StaticString("majority"), Validators: []validator.String{stringvalidator.OneOf("any", "majority", "all")},
			},
			"status_show_locations": schema.BoolAttribute{Description: "Break the check down by location on the status page (location names become public).", Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
		},
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
	switch kind.ValueString() {
	case "playwright":
		if !set("playwright") {
			resp.Diagnostics.AddAttributeError(path.Root("playwright"), "Missing playwright", "Playwright checks need the test's project and title.")
		}
		for _, n := range []string{"method", "expect_status", "body_contains", "headers", "steps", "secrets", "timeout_ms", "locations", "location_rule", "files", "trace_origins", "max_lcp_ms", "max_cls", "max_ttfb_ms", "max_duration_ms", "warn_lcp_ms", "warn_cls", "warn_ttfb_ms", "viewport_w", "viewport_h"} {
			if set(n) {
				resp.Diagnostics.AddAttributeError(path.Root(n), "Not for playwright checks", n+" does not apply: the test's own runs report the result.")
			}
		}
	case "browser":
		if !set("steps") {
			resp.Diagnostics.AddAttributeError(path.Root("steps"), "Missing steps", "Browser checks need steps, starting with goto.")
		}
		for _, n := range []string{"url", "method", "expect_status", "body_contains", "headers"} {
			if set(n) {
				resp.Diagnostics.AddAttributeError(path.Root(n), "Not for browser checks", n+" applies to http checks; a browser check starts at its first goto step.")
			}
		}
	default:
		if !set("url") {
			resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing url", "HTTP checks need a url.")
		}
		for _, n := range []string{"steps", "secrets", "files", "trace_origins", "max_lcp_ms", "max_cls", "max_ttfb_ms", "max_duration_ms", "warn_lcp_ms", "warn_cls", "warn_ttfb_ms", "viewport_w", "viewport_h"} {
			if set(n) {
				resp.Diagnostics.AddAttributeError(path.Root(n), "Only for browser checks", `Set kind = "browser" to use `+n+".")
			}
		}
	}
	if kind.ValueString() != "playwright" && set("playwright") {
		resp.Diagnostics.AddAttributeError(path.Root("playwright"), "Only for playwright checks", `Set kind = "playwright" to use playwright.`)
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
		Locations: stringsOf(ctx, m.Locations, diags), LocationRule: m.LocationRule.ValueString(), StatusShowLocs: m.StatusShowLocs.ValueBool(),
		TraceOrigins: stringsOf(ctx, m.TraceOrigins, diags), NoTrace: !m.TraceHeaders.IsNull() && !m.TraceHeaders.IsUnknown() && !m.TraceHeaders.ValueBool(),
		MaxLCPMs: m.MaxLCPMs.ValueInt64(), MaxCLS: m.MaxCLS.ValueFloat64(), MaxTTFBMs: m.MaxTTFBMs.ValueInt64(), MaxDurationMs: m.MaxDurationMs.ValueInt64(),
		WarnLCPMs: m.WarnLCPMs.ValueInt64(), WarnCLS: m.WarnCLS.ValueFloat64(), WarnTTFBMs: m.WarnTTFBMs.ValueInt64(),
		ViewportW: m.ViewportW.ValueInt64(), ViewportH: m.ViewportH.ValueInt64(), StepScreenshots: m.StepScreenshots.ValueBool(),
	}
	if in.Kind == "browser" {
		in.Device = m.Device.ValueString()
	}
	if in.Headers == nil {
		in.Headers = map[string]string{}
	}
	if m.Playwright != nil {
		in.Playwright = &apiPlaywright{Project: m.Playwright.Project.ValueString(), Title: m.Playwright.Title.ValueString()}
	}
	in.Tags = []string{}
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		diags.Append(m.Tags.ElementsAs(ctx, &in.Tags, false)...)
	}
	for _, s := range m.Steps {
		in.Steps = append(in.Steps, apiStepB{Action: s.Action.ValueString(), Selector: s.Selector.ValueString(), Text: s.Text.ValueString(),
			Value: s.Value.ValueString(), Count: s.Count.ValueInt64(), Op: s.Op.ValueString(), Ms: s.Ms.ValueInt64(), Label: s.Label.ValueString(),
			MaxMs: s.MaxMs.ValueInt64(), Name: s.Name.ValueString(), Attr: s.Attr.ValueString(), Regex: s.Regex.ValueString(), Target: s.Target.ValueString(),
			Scope: s.Scope.ValueString(), Permission: s.Permission.ValueString(), Lat: s.Lat.ValueFloat64(), Lon: s.Lon.ValueFloat64()})
	}
	for _, f := range m.Files {
		in.Files = append(in.Files, apiFile{Name: f.Name.ValueString(), Type: f.Type.ValueString(), Data: f.Content.ValueString()})
	}
	return in
}

// optDefault is optString for a field the server fills with def when left out.
func optDefault(v, def string, ref types.String) types.String {
	if v == def && ref.IsNull() {
		return types.StringNull()
	}
	return optString(v, ref)
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

func defaultScope(action string) string {
	if action == "set_storage" {
		return "local"
	}
	return ""
}

func fallbackStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (m *checkModel) fill(c apiCheck) {
	m.ID, m.Name, m.Kind, m.URL = idString(c.ID), types.StringValue(c.Name), types.StringValue(c.Kind), types.StringValue(c.URL)
	m.Method, m.ExpectStatus, m.BodyContains = types.StringValue(c.Method), types.StringValue(c.ExpectStatus), optString(c.BodyContains, m.BodyContains)
	m.Headers, m.Secrets = unmask(c.Headers, m.Headers), unmask(c.Secrets, m.Secrets)
	m.IntervalS, m.TimeoutMs, m.DegradedMs = types.Int64Value(c.IntervalS), types.Int64Value(c.TimeoutMs), optInt(c.DegradedMs, m.DegradedMs)
	m.FollowRedirects, m.Enabled = types.BoolValue(c.FollowRedirects), types.BoolValue(c.Enabled)
	m.OnStatusPage, m.StatusShowURL, m.FinalScreenshot = types.BoolValue(c.OnStatusPage), types.BoolValue(c.StatusShowURL), types.BoolValue(c.FinalScreenshot)
	m.TraceOrigins, m.TraceHeaders = optStringList(c.TraceOrigins, m.TraceOrigins), types.BoolValue(!c.NoTrace)
	m.MaxLCPMs, m.MaxCLS, m.MaxTTFBMs, m.MaxDurationMs = optInt(c.MaxLCPMs, m.MaxLCPMs), optFloat(c.MaxCLS, m.MaxCLS), optInt(c.MaxTTFBMs, m.MaxTTFBMs), optInt(c.MaxDurationMs, m.MaxDurationMs)
	m.WarnLCPMs, m.WarnCLS, m.WarnTTFBMs = optInt(c.WarnLCPMs, m.WarnLCPMs), optFloat(c.WarnCLS, m.WarnCLS), optInt(c.WarnTTFBMs, m.WarnTTFBMs)
	m.Device, m.ViewportW, m.ViewportH = types.StringValue(fallbackStr(c.Device, "desktop")), optInt(c.ViewportW, m.ViewportW), optInt(c.ViewportH, m.ViewportH)
	m.StepScreenshots = types.BoolValue(c.StepScreenshots)
	// The API leaves file contents out; they stay as configured.
	if len(c.Files) > 0 || m.Files != nil {
		known := map[string]fileModel{}
		for _, f := range m.Files {
			known[f.Name.ValueString()] = f
		}
		files := make([]fileModel, len(c.Files))
		for i, f := range c.Files {
			ref, ok := known[f.Name]
			if !ok {
				ref = fileModel{Type: types.StringNull(), Content: types.StringValue("")}
			}
			files[i] = fileModel{Name: types.StringValue(f.Name), Type: optString(f.Type, ref.Type), Content: ref.Content}
		}
		m.Files = files
	}
	m.Tags = optStringSet(c.Tags, m.Tags)
	m.Playwright = nil
	if c.Playwright != nil {
		m.Playwright = &pwModel{Project: types.StringValue(c.Playwright.Project), Title: types.StringValue(c.Playwright.Title)}
	}
	if len(c.Locations) == 0 {
		c.Locations = []string{"server"}
	}
	m.Locations = optStringList(c.Locations, m.Locations)
	if c.LocationRule == "" {
		c.LocationRule = "majority"
	}
	m.LocationRule, m.StatusShowLocs = types.StringValue(c.LocationRule), types.BoolValue(c.StatusShowLocs)
	if len(c.Steps) == 0 && m.Steps == nil {
		m.Steps = nil
		return
	}
	steps := make([]stepModel, len(c.Steps))
	for i, s := range c.Steps {
		ref := stepModel{Selector: types.StringNull(), Text: types.StringNull(), Value: types.StringNull(), Count: types.Int64Null(),
			Op: types.StringNull(), Ms: types.Int64Null(), Label: types.StringNull(), MaxMs: types.Int64Null(), Name: types.StringNull(),
			Attr: types.StringNull(), Regex: types.StringNull(), Target: types.StringNull(), Scope: types.StringNull(), Permission: types.StringNull(),
			Lat: types.Float64Null(), Lon: types.Float64Null()}
		if i < len(m.Steps) {
			ref = m.Steps[i]
		}
		steps[i] = stepModel{Action: types.StringValue(s.Action), Selector: optString(s.Selector, ref.Selector), Text: optString(s.Text, ref.Text),
			Value: optString(s.Value, ref.Value), Count: optInt(s.Count, ref.Count), Op: optString(s.Op, ref.Op), Ms: optInt(s.Ms, ref.Ms),
			Label: optString(s.Label, ref.Label), MaxMs: optInt(s.MaxMs, ref.MaxMs), Name: optString(s.Name, ref.Name),
			Attr: optDefault(s.Attr, "text", ref.Attr), Regex: optString(s.Regex, ref.Regex), Target: optString(s.Target, ref.Target),
			Scope: optDefault(s.Scope, defaultScope(s.Action), ref.Scope), Permission: optString(s.Permission, ref.Permission),
			Lat: optFloat(s.Lat, ref.Lat), Lon: optFloat(s.Lon, ref.Lon)}
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
