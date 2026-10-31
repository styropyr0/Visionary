package provider

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The check kinds beyond http and browser each have a block of their own:
// api, tcp, dns, ping, ssl and heartbeat.

var varNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

var checkKinds = []string{"http", "browser", "playwright", "api", "tcp", "dns", "ping", "ssl", "heartbeat"}

// kindBlocks are the blocks, by the kind that uses each.
var kindBlocks = []string{"api", "tcp", "dns", "ping", "ssl", "heartbeat"}

type apiModel struct {
	Requests []apiRequestModel `tfsdk:"requests"`
}

type apiRequestModel struct {
	Name      types.String     `tfsdk:"name"`
	Method    types.String     `tfsdk:"method"`
	URL       types.String     `tfsdk:"url"`
	Headers   types.Map        `tfsdk:"headers"`
	BodyType  types.String     `tfsdk:"body_type"`
	Body      types.String     `tfsdk:"body"`
	TimeoutMs types.Int64      `tfsdk:"timeout_ms"`
	Extract   []extractModel   `tfsdk:"extract"`
	Assert    []assertionModel `tfsdk:"assert"`
}

type extractModel struct {
	Var    types.String `tfsdk:"var"`
	Source types.String `tfsdk:"source"`
	Path   types.String `tfsdk:"path"`
}

type assertionModel struct {
	Source types.String `tfsdk:"source"`
	Path   types.String `tfsdk:"path"`
	Op     types.String `tfsdk:"op"`
	Value  types.String `tfsdk:"value"`
}

type tcpModel struct {
	Host   types.String `tfsdk:"host"`
	Port   types.Int64  `tfsdk:"port"`
	Send   types.String `tfsdk:"send"`
	Expect types.String `tfsdk:"expect"`
}

type dnsModel struct {
	Name       types.String `tfsdk:"name"`
	RecordType types.String `tfsdk:"record_type"`
	Server     types.String `tfsdk:"server"`
	Match      types.String `tfsdk:"match"`
	Values     types.List   `tfsdk:"values"`
}

type pingModel struct {
	Host       types.String `tfsdk:"host"`
	Packets    types.Int64  `tfsdk:"packets"`
	MaxLossPct types.Int64  `tfsdk:"max_loss_pct"`
}

type sslModel struct {
	Host           types.String `tfsdk:"host"`
	Port           types.Int64  `tfsdk:"port"`
	ServerName     types.String `tfsdk:"server_name"`
	WarnDays       types.Int64  `tfsdk:"warn_days"`
	FailDays       types.Int64  `tfsdk:"fail_days"`
	MinTLS         types.String `tfsdk:"min_tls"`
	IgnoreHostname types.Bool   `tfsdk:"ignore_hostname"`
	IgnoreChain    types.Bool   `tfsdk:"ignore_chain"`
}

type heartbeatModel struct {
	PeriodS  types.Int64  `tfsdk:"period_s"`
	Cron     types.String `tfsdk:"cron"`
	Timezone types.String `tfsdk:"timezone"`
	GraceS   types.Int64  `tfsdk:"grace_s"`
}

// The API's shapes.

type apiAPI struct {
	Requests []apiRequestB `json:"requests"`
}

type apiRequestB struct {
	Name      string            `json:"name,omitempty"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	BodyType  string            `json:"body_type,omitempty"`
	Body      string            `json:"body,omitempty"`
	TimeoutMs int64             `json:"timeout_ms,omitempty"`
	Extract   []apiExtractB     `json:"extract,omitempty"`
	Assert    []apiAssertionB   `json:"assert,omitempty"`
}

type apiExtractB struct {
	Var    string `json:"var"`
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
}

type apiAssertionB struct {
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
	Op     string `json:"op"`
	Value  string `json:"value,omitempty"`
}

type apiTCP struct {
	Host   string `json:"host"`
	Port   int64  `json:"port"`
	Send   string `json:"send,omitempty"`
	Expect string `json:"expect,omitempty"`
}

type apiDNS struct {
	Name       string   `json:"name"`
	RecordType string   `json:"record_type"`
	Server     string   `json:"server,omitempty"`
	Match      string   `json:"match,omitempty"`
	Values     []string `json:"values,omitempty"`
}

type apiPing struct {
	Host       string `json:"host"`
	Packets    int64  `json:"packets"`
	MaxLossPct int64  `json:"max_loss_pct"`
}

type apiSSL struct {
	Host           string `json:"host"`
	Port           int64  `json:"port"`
	ServerName     string `json:"server_name,omitempty"`
	WarnDays       int64  `json:"warn_days"`
	FailDays       int64  `json:"fail_days"`
	MinTLS         string `json:"min_tls,omitempty"`
	IgnoreHostname bool   `json:"ignore_hostname,omitempty"`
	IgnoreChain    bool   `json:"ignore_chain,omitempty"`
}

type apiHeartbeat struct {
	PeriodS  int64  `json:"period_s,omitempty"`
	Cron     string `json:"cron,omitempty"`
	Timezone string `json:"timezone,omitempty"`
	GraceS   int64  `json:"grace_s"`
}

func hostAttr(desc string) schema.StringAttribute {
	return schema.StringAttribute{Description: desc, Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 253)}}
}

func portAttr(desc string, required bool) schema.Int64Attribute {
	a := schema.Int64Attribute{Description: desc, Validators: []validator.Int64{int64validator.Between(1, 65535)}}
	if required {
		a.Required = true
	} else {
		a.Optional, a.Computed, a.Default = true, true, int64default.StaticInt64(443)
	}
	return a
}

func kindAttributes() map[string]schema.Attribute {
	extract := map[string]schema.Attribute{
		"var":    schema.StringAttribute{Description: "Variable name, used later as {{var.NAME}}.", Required: true, Validators: []validator.String{stringvalidator.RegexMatches(varNameRe, "use letters, digits and _")}},
		"source": schema.StringAttribute{Description: "json, header, regex or status.", Required: true, Validators: []validator.String{stringvalidator.OneOf("json", "header", "regex", "status")}},
		"path":   optStr("JSON path ($.data.token), header name or regular expression (its first group is kept)."),
	}
	assertion := map[string]schema.Attribute{
		"source": schema.StringAttribute{Description: "status, json, header, body or time.", Required: true, Validators: []validator.String{stringvalidator.OneOf("status", "json", "header", "body", "time")}},
		"path":   optStr("JSON path (json) or header name (header)."),
		"op": schema.StringAttribute{Description: "in (status), equals, not_equals, contains, not_contains, exists, not_exists, lt, lte, gt, gte or matches.", Required: true,
			Validators: []validator.String{stringvalidator.OneOf("in", "equals", "not_equals", "contains", "not_contains", "exists", "not_exists", "lt", "lte", "gt", "gte", "matches")}},
		"value": optStr("Expected value: statuses such as 200-299 for in, a number, text or a regular expression."),
	}
	request := map[string]schema.Attribute{
		"name": optStr("Name shown in results."),
		"method": schema.StringAttribute{Description: "HTTP method (default GET).", Optional: true, Computed: true, Default: stringdefault.StaticString("GET"),
			Validators: []validator.String{stringvalidator.OneOf("GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS")}},
		"url":        schema.StringAttribute{Description: "Absolute http(s) URL; may use {{var.NAME}} and {{secret.NAME}}.", Required: true},
		"headers":    schema.MapAttribute{Description: "Request headers; values may use {{var.NAME}} and {{secret.NAME}}.", Optional: true, Sensitive: true, ElementType: types.StringType},
		"body_type":  schema.StringAttribute{Description: "json, text or form.", Optional: true, Validators: []validator.String{stringvalidator.OneOf("json", "text", "form")}},
		"body":       optStr("Request body; may use {{var.NAME}} and {{secret.NAME}}."),
		"timeout_ms": schema.Int64Attribute{Description: "Timeout of this request in ms (default: the check's).", Optional: true, Validators: []validator.Int64{int64validator.Between(1000, 60000)}},
		"extract": schema.ListNestedAttribute{Description: "Values saved from the response for later requests.", Optional: true,
			Validators: []validator.List{listvalidator.SizeAtMost(10)}, NestedObject: schema.NestedAttributeObject{Attributes: extract}},
		"assert": schema.ListNestedAttribute{Description: "Conditions on the response. Without a status assertion, a status of 400 or more fails.", Optional: true,
			Validators: []validator.List{listvalidator.SizeAtMost(20)}, NestedObject: schema.NestedAttributeObject{Attributes: assertion}},
	}
	return map[string]schema.Attribute{
		"api": schema.SingleNestedAttribute{
			Description: "Multi-step API check (kind api): requests run in order and share variables.",
			Optional:    true,
			Attributes: map[string]schema.Attribute{
				"requests": schema.ListNestedAttribute{Description: "The requests, 1 to 10.", Required: true,
					Validators: []validator.List{listvalidator.SizeBetween(1, 10)}, NestedObject: schema.NestedAttributeObject{Attributes: request}},
			},
		},
		"tcp": schema.SingleNestedAttribute{
			Description: "TCP check (kind tcp): the port accepts a connection, and optionally answers what is sent.",
			Optional:    true,
			Attributes: map[string]schema.Attribute{
				"host":   hostAttr("Host name or IP address."),
				"port":   portAttr("Port.", true),
				"send":   optStr(`Text sent after connecting; \r, \n and \t are read as escapes.`),
				"expect": optStr("Text the reply must contain, such as SSH-2.0."),
			},
		},
		"dns": schema.SingleNestedAttribute{
			Description: "DNS check (kind dns): the name resolves, optionally to given values.",
			Optional:    true,
			Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{Description: "Name to resolve.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 253)}},
				"record_type": schema.StringAttribute{Description: "A (default), AAAA, CNAME, MX, TXT or NS.", Optional: true, Computed: true, Default: stringdefault.StaticString("A"),
					Validators: []validator.String{stringvalidator.OneOf("A", "AAAA", "CNAME", "MX", "TXT", "NS")}},
				"server": optStr("DNS server (IP or host, optional :port); the system resolver when unset."),
				"match":  schema.StringAttribute{Description: "contains (every value is an answer) or equals (the answers are exactly the values).", Optional: true, Validators: []validator.String{stringvalidator.OneOf("contains", "equals")}},
				"values": schema.ListAttribute{Description: "Expected answers.", Optional: true, ElementType: types.StringType, Validators: []validator.List{listvalidator.SizeBetween(1, 20)}},
			},
		},
		"ping": schema.SingleNestedAttribute{
			Description: "Ping check (kind ping): ICMP echo, reporting loss and round trip time. timeout_ms is each reply's wait (default 2000).",
			Optional:    true,
			Attributes: map[string]schema.Attribute{
				"host":         hostAttr("Host name or IP address."),
				"packets":      schema.Int64Attribute{Description: "Packets per run, 1 to 20 (default 4).", Optional: true, Computed: true, Default: int64default.StaticInt64(4), Validators: []validator.Int64{int64validator.Between(1, 20)}},
				"max_loss_pct": schema.Int64Attribute{Description: "Fails above this packet loss in percent, 0 to 99 (default 50).", Optional: true, Computed: true, Default: int64default.StaticInt64(50), Validators: []validator.Int64{int64validator.Between(0, 99)}},
			},
		},
		"ssl": schema.SingleNestedAttribute{
			Description: "Certificate check (kind ssl): validity, days to expiry, hostname, chain and TLS version, without an HTTP request.",
			Optional:    true,
			Attributes: map[string]schema.Attribute{
				"host":            hostAttr("Host name or IP address."),
				"port":            portAttr("Port (default 443).", false),
				"server_name":     optStr("Name sent as SNI and checked against the certificate (default: host)."),
				"warn_days":       schema.Int64Attribute{Description: "Degraded below this many days left (default 14).", Optional: true, Computed: true, Default: int64default.StaticInt64(14), Validators: []validator.Int64{int64validator.Between(0, 365)}},
				"fail_days":       schema.Int64Attribute{Description: "Fails below this many days left (default 0: only once expired).", Optional: true, Computed: true, Default: int64default.StaticInt64(0), Validators: []validator.Int64{int64validator.Between(0, 365)}},
				"min_tls":         schema.StringAttribute{Description: "Minimum TLS version: 1.0, 1.1, 1.2 or 1.3.", Optional: true, Validators: []validator.String{stringvalidator.OneOf("1.0", "1.1", "1.2", "1.3")}},
				"ignore_hostname": schema.BoolAttribute{Description: "Pass even when the certificate does not name the host.", Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
				"ignore_chain":    schema.BoolAttribute{Description: "Pass even when the chain is not trusted (self-signed).", Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
			},
		},
		"heartbeat": schema.SingleNestedAttribute{
			Description: "Heartbeat check (kind heartbeat): a job calls its URL; it is down when no ping arrives in time or the job reports a failure.",
			Optional:    true,
			Attributes: map[string]schema.Attribute{
				"period_s": schema.Int64Attribute{Description: "Seconds between pings, 60 to 2678400. Give this or cron.", Optional: true, Validators: []validator.Int64{int64validator.Between(60, 31*24*3600)}},
				"cron":     optStr("Cron schedule of the job, five fields (or @hourly, @daily...)."),
				"timezone": schema.StringAttribute{Description: "Time zone of cron, such as Europe/Berlin (default UTC).", Optional: true},
				"grace_s":  schema.Int64Attribute{Description: "Seconds a ping may be late, 0 to 604800 (default 300).", Optional: true, Computed: true, Default: int64default.StaticInt64(300), Validators: []validator.Int64{int64validator.Between(0, 7*24*3600)}},
			},
		},
		"heartbeat_path": schema.StringAttribute{
			Description: "Path the job calls (heartbeat checks), such as /api/heartbeat/<token>; append /start, /fail or an exit code. Prefix the server's address.",
			Computed:    true, Sensitive: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
	}
}

// kindProblems says what a check of kind cannot have, or lacks, by
// attribute; set reports which attributes the configuration sets.
func kindProblems(kind string, set func(string) bool) map[string]string {
	out := map[string]string{}
	block := map[string]string{"api": "api", "tcp": "tcp", "dns": "dns", "ping": "ping", "ssl": "ssl", "heartbeat": "heartbeat"}[kind]
	for _, b := range kindBlocks {
		if b == block && !set(b) {
			out[b] = kind + " checks need a " + b + " block."
		}
		if b != block && set(b) {
			out[b] = `Set kind = "` + b + `" to use the ` + b + " block."
		}
	}
	if kind != "playwright" && set("playwright") {
		out["playwright"] = `Set kind = "playwright" to use the playwright block.`
	}
	switch kind {
	case "playwright":
		if !set("playwright") {
			out["playwright"] = "Playwright checks need the test's project and title."
		}
		for _, n := range []string{"method", "expect_status", "body_contains", "headers", "steps", "secrets", "timeout_ms", "locations", "location_rule"} {
			if set(n) {
				out[n] = n + " does not apply: the test's own runs report the result."
			}
		}
	case "http":
		if !set("url") {
			out["url"] = "HTTP checks need a url."
		}
		for _, n := range []string{"steps", "secrets"} {
			if set(n) {
				out[n] = `Set kind = "browser" (or "api" for secrets) to use ` + n + "."
			}
		}
	case "browser":
		if !set("steps") {
			out["steps"] = "Browser checks need steps, starting with goto."
		}
		for _, n := range []string{"url", "method", "expect_status", "body_contains", "headers"} {
			if set(n) {
				out[n] = n + " applies to http checks; a browser check starts at its first goto step."
			}
		}
	default:
		for _, n := range []string{"url", "method", "expect_status", "body_contains", "headers", "steps"} {
			if set(n) {
				out[n] = n + " does not apply to " + kind + " checks; set it in the " + block + " block."
			}
		}
		if kind != "api" && set("secrets") {
			out["secrets"] = "secrets apply to browser and api checks."
		}
		if kind == "heartbeat" {
			for _, n := range []string{"interval_s", "timeout_ms", "degraded_ms"} {
				if set(n) {
					out[n] = n + " does not apply to heartbeat checks: pings are looked for every minute."
				}
			}
			for _, n := range []string{"locations", "location_rule", "status_show_locations"} {
				if set(n) {
					out[n] = n + " does not apply to heartbeat checks: the job pings the server."
				}
			}
		}
	}
	return out
}

func (m *checkModel) kindInput(ctx context.Context, in *apiCheck, diags *diag.Diagnostics) {
	if a := m.API; a != nil {
		in.API = &apiAPI{}
		for _, r := range a.Requests {
			rq := apiRequestB{Name: r.Name.ValueString(), Method: r.Method.ValueString(), URL: r.URL.ValueString(), Headers: mapOf(ctx, r.Headers, diags),
				BodyType: r.BodyType.ValueString(), Body: r.Body.ValueString(), TimeoutMs: r.TimeoutMs.ValueInt64()}
			for _, e := range r.Extract {
				rq.Extract = append(rq.Extract, apiExtractB{Var: e.Var.ValueString(), Source: e.Source.ValueString(), Path: e.Path.ValueString()})
			}
			for _, x := range r.Assert {
				rq.Assert = append(rq.Assert, apiAssertionB{Source: x.Source.ValueString(), Path: x.Path.ValueString(), Op: x.Op.ValueString(), Value: x.Value.ValueString()})
			}
			in.API.Requests = append(in.API.Requests, rq)
		}
	}
	if t := m.TCP; t != nil {
		in.TCP = &apiTCP{Host: t.Host.ValueString(), Port: t.Port.ValueInt64(), Send: t.Send.ValueString(), Expect: t.Expect.ValueString()}
	}
	if d := m.DNS; d != nil {
		in.DNS = &apiDNS{Name: d.Name.ValueString(), RecordType: d.RecordType.ValueString(), Server: d.Server.ValueString(), Match: d.Match.ValueString(), Values: stringsOf(ctx, d.Values, diags)}
	}
	if p := m.Ping; p != nil {
		in.Ping = &apiPing{Host: p.Host.ValueString(), Packets: p.Packets.ValueInt64(), MaxLossPct: p.MaxLossPct.ValueInt64()}
	}
	if s := m.SSL; s != nil {
		in.SSL = &apiSSL{Host: s.Host.ValueString(), Port: s.Port.ValueInt64(), ServerName: s.ServerName.ValueString(), WarnDays: s.WarnDays.ValueInt64(),
			FailDays: s.FailDays.ValueInt64(), MinTLS: s.MinTLS.ValueString(), IgnoreHostname: s.IgnoreHostname.ValueBool(), IgnoreChain: s.IgnoreChain.ValueBool()}
	}
	if h := m.Heartbeat; h != nil {
		in.Heartbeat = &apiHeartbeat{PeriodS: h.PeriodS.ValueInt64(), Cron: h.Cron.ValueString(), Timezone: h.Timezone.ValueString(), GraceS: h.GraceS.ValueInt64()}
	}
}

// readHeartbeatPath fills heartbeat_path, which the check itself never
// shows.
func (r *uptimeCheckResource) readHeartbeatPath(ctx context.Context, m *checkModel, diags *diag.Diagnostics) {
	if m.Kind.ValueString() != "heartbeat" {
		m.HeartbeatPath = types.StringNull()
		return
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := r.c.do(ctx, http.MethodGet, "/api/uptime/checks/"+m.ID.ValueString()+"/heartbeat", nil, &out); err != nil {
		apiDiag(diags, "read heartbeat URL", err)
		return
	}
	m.HeartbeatPath = types.StringValue(out.Path)
}

// sameFold keeps the written form of a name the server lower-cases.
func sameFold(v string, ref types.String) types.String {
	if !ref.IsNull() && !ref.IsUnknown() && strings.EqualFold(strings.TrimSuffix(ref.ValueString(), "."), v) {
		return ref
	}
	return types.StringValue(v)
}

func (m *checkModel) kindFill(c apiCheck) {
	if c.API == nil {
		m.API = nil
	} else {
		var ref []apiRequestModel
		if m.API != nil {
			ref = m.API.Requests
		}
		out := &apiModel{}
		for i, r := range c.API.Requests {
			old := apiRequestModel{Name: types.StringNull(), Headers: types.MapNull(types.StringType), BodyType: types.StringNull(), Body: types.StringNull(), TimeoutMs: types.Int64Null()}
			if i < len(ref) {
				old = ref[i]
			}
			rm := apiRequestModel{Name: optString(r.Name, old.Name), Method: types.StringValue(r.Method), URL: types.StringValue(r.URL), Headers: unmask(r.Headers, old.Headers),
				BodyType: optString(r.BodyType, old.BodyType), Body: optString(r.Body, old.Body), TimeoutMs: optInt(r.TimeoutMs, old.TimeoutMs)}
			for j, e := range r.Extract {
				p := types.StringNull()
				if j < len(old.Extract) {
					p = old.Extract[j].Path
				}
				rm.Extract = append(rm.Extract, extractModel{Var: types.StringValue(e.Var), Source: types.StringValue(e.Source), Path: optString(e.Path, p)})
			}
			for j, x := range r.Assert {
				p, v := types.StringNull(), types.StringNull()
				if j < len(old.Assert) {
					p, v = old.Assert[j].Path, old.Assert[j].Value
				}
				rm.Assert = append(rm.Assert, assertionModel{Source: types.StringValue(x.Source), Path: optString(x.Path, p), Op: types.StringValue(x.Op), Value: optString(x.Value, v)})
			}
			out.Requests = append(out.Requests, rm)
		}
		m.API = out
	}
	if c.TCP == nil {
		m.TCP = nil
	} else {
		old := tcpModel{Host: types.StringNull(), Send: types.StringNull(), Expect: types.StringNull()}
		if m.TCP != nil {
			old = *m.TCP
		}
		m.TCP = &tcpModel{Host: sameFold(c.TCP.Host, old.Host), Port: types.Int64Value(c.TCP.Port), Send: optString(c.TCP.Send, old.Send), Expect: optString(c.TCP.Expect, old.Expect)}
	}
	if c.DNS == nil {
		m.DNS = nil
	} else {
		old := dnsModel{Name: types.StringNull(), Server: types.StringNull(), Match: types.StringNull(), Values: types.ListNull(types.StringType)}
		if m.DNS != nil {
			old = *m.DNS
		}
		server := optString(c.DNS.Server, old.Server)
		if !old.Server.IsNull() && strings.Trim(strings.TrimSuffix(c.DNS.Server, ":53"), "[]") == strings.Trim(old.Server.ValueString(), "[]") {
			server = old.Server
		}
		m.DNS = &dnsModel{Name: sameFold(c.DNS.Name, old.Name), RecordType: types.StringValue(c.DNS.RecordType), Server: server,
			Match: optString(c.DNS.Match, old.Match), Values: optStringList(c.DNS.Values, old.Values)}
	}
	if c.Ping == nil {
		m.Ping = nil
	} else {
		old := pingModel{Host: types.StringNull()}
		if m.Ping != nil {
			old = *m.Ping
		}
		m.Ping = &pingModel{Host: sameFold(c.Ping.Host, old.Host), Packets: types.Int64Value(c.Ping.Packets), MaxLossPct: types.Int64Value(c.Ping.MaxLossPct)}
	}
	if c.SSL == nil {
		m.SSL = nil
	} else {
		old := sslModel{Host: types.StringNull(), ServerName: types.StringNull(), MinTLS: types.StringNull()}
		if m.SSL != nil {
			old = *m.SSL
		}
		s := c.SSL
		m.SSL = &sslModel{Host: sameFold(s.Host, old.Host), Port: types.Int64Value(s.Port), ServerName: optString(s.ServerName, old.ServerName),
			WarnDays: types.Int64Value(s.WarnDays), FailDays: types.Int64Value(s.FailDays), MinTLS: optString(s.MinTLS, old.MinTLS),
			IgnoreHostname: types.BoolValue(s.IgnoreHostname), IgnoreChain: types.BoolValue(s.IgnoreChain)}
	}
	if c.Heartbeat == nil {
		m.Heartbeat = nil
	} else {
		old := heartbeatModel{PeriodS: types.Int64Null(), Cron: types.StringNull(), Timezone: types.StringNull()}
		if m.Heartbeat != nil {
			old = *m.Heartbeat
		}
		h := c.Heartbeat
		tz := optString(h.Timezone, old.Timezone)
		if old.Timezone.IsNull() && h.Timezone == "UTC" {
			tz = types.StringNull()
		}
		m.Heartbeat = &heartbeatModel{PeriodS: optInt(h.PeriodS, old.PeriodS), Cron: optString(h.Cron, old.Cron), Timezone: tz, GraceS: types.Int64Value(h.GraceS)}
	}
}
