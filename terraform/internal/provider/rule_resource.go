package provider

import (
	"context"
	"net/http"
	"slices"

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
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type alertRuleResource struct{ base }

func newAlertRuleResource() resource.Resource { return &alertRuleResource{} }

var (
	_ resource.ResourceWithImportState    = (*alertRuleResource)(nil)
	_ resource.ResourceWithValidateConfig = (*alertRuleResource)(nil)
)

type ruleModel struct {
	ID             types.String      `tfsdk:"id"`
	Name           types.String      `tfsdk:"name"`
	Kind           types.String      `tfsdk:"kind"`
	Severity       types.String      `tfsdk:"severity"`
	Channels       types.List        `tfsdk:"channels"`
	Runbook        types.String      `tfsdk:"runbook"`
	Enabled        types.Bool        `tfsdk:"enabled"`
	Metric         types.String      `tfsdk:"metric"`
	SQL            types.String      `tfsdk:"sql"`
	Query          types.String      `tfsdk:"query"`
	PromQL         types.String      `tfsdk:"promql"`
	MetricQuery    *metricQueryModel `tfsdk:"metric_query"`
	Scope          *scopeModel       `tfsdk:"scope"`
	Op             types.String      `tfsdk:"op"`
	Threshold      types.Float64     `tfsdk:"threshold"`
	Window         types.String      `tfsdk:"window"`
	For            types.String      `tfsdk:"for"`
	Repeat         types.String      `tfsdk:"repeat"`
	Sensitivity    types.String      `tfsdk:"sensitivity"`
	Direction      types.String      `tfsdk:"direction"`
	Target         types.Float64     `tfsdk:"target"`
	NoData         types.String      `tfsdk:"no_data"`
	ClearFor       types.String      `tfsdk:"clear_for"`
	ClearThreshold types.Float64     `tfsdk:"clear_threshold"`
	NotifyResolved types.Bool        `tfsdk:"notify_resolved"`
	Mention        types.String      `tfsdk:"mention"`
	Title          types.String      `tfsdk:"title"`
	Message        types.String      `tfsdk:"message"`
	Labels         types.Map         `tfsdk:"labels"`
	DependsOn      types.List        `tfsdk:"depends_on_rules"`
	Escalation     []escalationModel `tfsdk:"escalation"`
}

type metricQueryModel struct {
	Name   types.String `tfsdk:"name"`
	Agg    types.String `tfsdk:"agg"`
	Group  types.String `tfsdk:"group"`
	Labels types.Map    `tfsdk:"labels"`
}

type scopeModel struct {
	Route         types.String `tfsdk:"route"`
	Method        types.String `tfsdk:"method"`
	Instance      types.String `tfsdk:"instance"`
	Service       types.String `tfsdk:"service"`
	Check         types.String `tfsdk:"check"`
	EventKind     types.String `tfsdk:"event_kind"`
	EventSeverity types.String `tfsdk:"event_severity"`
	Source        types.String `tfsdk:"source"`
	Host          types.String `tfsdk:"host"`
	Container     types.String `tfsdk:"container"`
	App           types.String `tfsdk:"app"`
}

type escalationModel struct {
	After    types.String `tfsdk:"after"`
	Channels types.List   `tfsdk:"channels"`
}

type apiRule struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Kind     string  `json:"kind"`
	Severity string  `json:"severity"`
	Channels []int64 `json:"channels"`
	Runbook  string  `json:"runbook"`
	Enabled  bool    `json:"enabled"`
	Spec     apiSpec `json:"spec"`
}

type apiSpec struct {
	Metric         string            `json:"metric"`
	SQL            string            `json:"sql,omitempty"`
	Query          string            `json:"query,omitempty"`
	Scope          apiScope          `json:"scope"`
	Op             string            `json:"op,omitempty"`
	Threshold      float64           `json:"threshold"`
	Window         string            `json:"window"`
	For            string            `json:"for"`
	MetricQuery    *apiMetricQuery   `json:"metric_query,omitempty"`
	PromQL         string            `json:"promql,omitempty"`
	Repeat         string            `json:"repeat,omitempty"`
	Sensitivity    string            `json:"sensitivity,omitempty"`
	Direction      string            `json:"direction,omitempty"`
	Target         float64           `json:"target,omitempty"`
	NoData         string            `json:"no_data,omitempty"`
	ClearFor       string            `json:"clear_for,omitempty"`
	ClearThreshold *float64          `json:"clear_threshold,omitempty"`
	NotifyResolved *bool             `json:"notify_resolved,omitempty"`
	Mention        string            `json:"mention,omitempty"`
	Title          string            `json:"title,omitempty"`
	Message        string            `json:"message,omitempty"`
	Escalation     []apiStep         `json:"escalation,omitempty"`
	DependsOn      []int64           `json:"depends_on,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
}

type apiScope struct {
	Route         string `json:"route,omitempty"`
	Method        string `json:"method,omitempty"`
	Instance      string `json:"instance,omitempty"`
	Service       string `json:"service,omitempty"`
	Check         string `json:"check,omitempty"`
	EventKind     string `json:"event_kind,omitempty"`
	EventSeverity string `json:"event_severity,omitempty"`
	Source        string `json:"source,omitempty"`
	Host          string `json:"host,omitempty"`
	Container     string `json:"container,omitempty"`
	App           string `json:"app,omitempty"`
}

type apiMetricQuery struct {
	Name   string            `json:"name"`
	Agg    string            `json:"agg,omitempty"`
	Group  string            `json:"group,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

type apiStep struct {
	After    string  `json:"after"`
	Channels []int64 `json:"channels"`
}

type ruleInput struct {
	Name     string  `json:"name"`
	Kind     string  `json:"kind"`
	Severity string  `json:"severity"`
	Channels []int64 `json:"channels"`
	Runbook  string  `json:"runbook"`
	Enabled  bool    `json:"enabled"`
	Spec     apiSpec `json:"spec"`
}

func (r *alertRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_rule"
}

func optStr(desc string) schema.StringAttribute {
	return schema.StringAttribute{Description: desc, Optional: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}}
}

func (r *alertRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	scope := map[string]schema.Attribute{}
	for name, desc := range map[string]string{
		"route": "Route pattern, such as /orders/{id}.", "method": "HTTP method.", "instance": "Instance name.", "service": "Service name.",
		"check": "Uptime check id, for uptime metrics.", "event_kind": "Security finding kind.", "event_severity": "Lowest security finding severity.",
		"source": "Security finding source: api or host.", "host": "Host name.", "container": "Container name.", "app": "Client app.",
	} {
		scope[name] = optStr(desc)
	}
	resp.Schema = schema.Schema{
		Description: "An alert rule: a threshold on a metric, an anomaly on one, or an SLO burn rate. " +
			"The metric ids are listed in Alerts → New rule and by GET /api/alerts/meta.",
		Attributes: map[string]schema.Attribute{
			"id":   idAttribute(),
			"name": schema.StringAttribute{Description: "Name.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 120)}},
			"kind": schema.StringAttribute{
				Description: "threshold (default), anomaly or slo.", Optional: true, Computed: true, Default: stringdefault.StaticString("threshold"),
				Validators: []validator.String{stringvalidator.OneOf("threshold", "anomaly", "slo")},
			},
			"severity": schema.StringAttribute{
				Description: "info, warning (default) or critical.", Optional: true, Computed: true, Default: stringdefault.StaticString("warning"),
				Validators: []validator.String{stringvalidator.OneOf("info", "warning", "critical")},
			},
			"channels": schema.ListAttribute{Description: "Ids of the channels notified.", Optional: true, ElementType: types.StringType},
			"runbook":  optStr("A note or link shown with the alert."),
			"enabled":  schema.BoolAttribute{Description: "Whether the rule is evaluated. Default true.", Optional: true, Computed: true, Default: booldefault.StaticBool(true)},
			"metric": schema.StringAttribute{
				Description: "Metric id, such as latency_p95, error_rate, log_matches, custom (with sql), custom_metric (with metric_query) or promql (with promql). SLO rules have none.",
				Optional:    true, Computed: true, PlanModifiers: []planmodifier.String{keepIf("kind")},
			},
			"sql":    optStr("Read-only SQL returning one number (metric custom)."),
			"query":  optStr("Log search (metrics log_matches and log_absent)."),
			"promql": optStr("PromQL expression (metric promql)."),
			"metric_query": schema.SingleNestedAttribute{
				Description: "The custom metric watched (metric custom_metric).",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"name":   schema.StringAttribute{Description: "Metric name.", Required: true},
					"agg":    schema.StringAttribute{Description: "sum, avg, max, min, rate, count, p50, p95 or p99.", Optional: true, Validators: []validator.String{stringvalidator.OneOf("sum", "avg", "max", "min", "rate", "count", "p50", "p95", "p99")}},
					"group":  optStr("Label to alert on separately for each value."),
					"labels": schema.MapAttribute{Description: "Label filters.", Optional: true, ElementType: types.StringType},
				},
			},
			"scope":     schema.SingleNestedAttribute{Description: "What the rule looks at; empty means everything.", Optional: true, Attributes: scope},
			"op":        schema.StringAttribute{Description: "Condition: >, <, >= or <= (threshold rules).", Optional: true, Validators: []validator.String{stringvalidator.OneOf(">", "<", ">=", "<=")}},
			"threshold": schema.Float64Attribute{Description: "Threshold (threshold rules).", Optional: true},
			"window": schema.StringAttribute{Description: "Evaluation window, such as 5m (default 5m; 15m for anomaly rules).", Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{keepIf("kind")}},
			"for":    optStr("How long the condition must hold before firing, such as 2m."),
			"repeat": optStr("Re-send a firing alert this often (5m to 7d)."),
			"sensitivity": schema.StringAttribute{
				Description: "Anomaly sensitivity: low, medium (default) or high.", Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{keepIf("kind")},
				Validators:    []validator.String{stringvalidator.OneOf("low", "medium", "high")},
			},
			"direction": schema.StringAttribute{Description: "Anomaly direction: up, down or both.", Optional: true, Validators: []validator.String{stringvalidator.OneOf("up", "down", "both")}},
			"target": schema.Float64Attribute{Description: "SLO target percent (default: Settings → Thresholds).", Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Float64{keepIf("kind")}},
			"no_data":          schema.StringAttribute{Description: "A window without data counts as ok (default), alerting or keep.", Optional: true, Validators: []validator.String{stringvalidator.OneOf("ok", "alerting", "keep")}},
			"clear_for":        optStr("How long the condition must stay clear before resolving."),
			"clear_threshold":  schema.Float64Attribute{Description: "Value a reading must pass to count as clear (threshold rules).", Optional: true},
			"notify_resolved":  schema.BoolAttribute{Description: "Notify when resolved. Default true.", Optional: true, Computed: true, Default: booldefault.StaticBool(true)},
			"mention":          optStr("Starts every message, such as <!here> or @oncall."),
			"title":            optStr("Message title template."),
			"message":          optStr("Message body template."),
			"labels":           schema.MapAttribute{Description: "Labels, for silences and routing.", Optional: true, ElementType: types.StringType},
			"depends_on_rules": schema.ListAttribute{Description: "Ids of rules that hold this one's notifications back while they fire.", Optional: true, ElementType: types.StringType, Validators: []validator.List{listvalidator.SizeAtMost(10)}},
			"escalation": schema.ListNestedAttribute{
				Description: "Steps that notify more channels when nobody acknowledges.",
				Optional:    true,
				Validators:  []validator.List{listvalidator.SizeBetween(1, 5)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"after":    schema.StringAttribute{Description: "Wait, such as 15m.", Required: true},
					"channels": schema.ListAttribute{Description: "Channel ids.", Required: true, ElementType: types.StringType, Validators: []validator.List{listvalidator.SizeAtLeast(1)}},
				}},
			},
		},
	}
}

// ruleCheck is a rule's configuration as plan-time validation sees it; any
// value may still be unknown.
type ruleCheck struct {
	kind, metric                                                  string
	set                                                           func(name string) bool
	instanceInScope, checkInScope, anyScope, metricQuery, unknown bool
}

func readRuleCheck(ctx context.Context, cfg tfsdk.Config) (ruleCheck, diag.Diagnostics) {
	var diags diag.Diagnostics
	rc := ruleCheck{kind: "threshold"}
	str := func(p path.Path) (string, bool) {
		var v types.String
		diags.Append(cfg.GetAttribute(ctx, p, &v)...)
		rc.unknown = rc.unknown || v.IsUnknown()
		return v.ValueString(), !v.IsNull()
	}
	if k, ok := str(path.Root("kind")); ok {
		rc.kind = k
	}
	rc.metric, _ = str(path.Root("metric"))
	rc.set = func(name string) bool {
		var v attr.Value
		diags.Append(cfg.GetAttribute(ctx, path.Root(name), &v)...)
		return v != nil && !v.IsNull()
	}
	var scope types.Object
	diags.Append(cfg.GetAttribute(ctx, path.Root("scope"), &scope)...)
	if !scope.IsNull() && !scope.IsUnknown() {
		rc.anyScope = true
		_, rc.instanceInScope = str(path.Root("scope").AtName("instance"))
		_, rc.checkInScope = str(path.Root("scope").AtName("check"))
	}
	rc.metricQuery = rc.set("metric_query")
	return rc, diags
}

// problems lists what does not fit the rule's kind and metric: the server
// would drop it, and the plan would not match what it stores.
func (rc ruleCheck) problems() map[string]string {
	out := map[string]string{}
	not := func(why string, names ...string) {
		for _, n := range names {
			if rc.set(n) {
				out[n] = why
			}
		}
	}
	switch rc.kind {
	case "threshold":
		if rc.metric == "" && !rc.unknown {
			out["metric"] = "threshold rules need a metric"
		}
		if !rc.set("op") {
			out["op"] = "threshold rules need op"
		}
		not("only for anomaly and SLO rules", "sensitivity", "direction", "target")
		if rc.metric != "custom" && rc.set("sql") {
			out["sql"] = "sql goes with metric custom"
		}
		if rc.metric == "custom" && !rc.set("sql") {
			out["sql"] = "metric custom needs sql"
		}
		if rc.metric != "log_matches" && rc.metric != "log_absent" && rc.set("query") {
			out["query"] = "query goes with metrics log_matches and log_absent"
		}
		if rc.metric != "promql" && rc.set("promql") {
			out["promql"] = "promql goes with metric promql"
		}
		if rc.metric == "promql" && !rc.set("promql") {
			out["promql"] = "metric promql needs promql"
		}
		if rc.metric != "custom_metric" && rc.metricQuery {
			out["metric_query"] = "metric_query goes with metric custom_metric"
		}
		if rc.metric == "custom_metric" && !rc.metricQuery {
			out["metric_query"] = "metric custom_metric needs metric_query"
		}
		if (rc.metric == "custom" || rc.metric == "promql") && rc.anyScope {
			out["scope"] = "custom SQL and PromQL rules have no scope"
		}
	case "anomaly":
		if rc.metric == "" && !rc.unknown {
			out["metric"] = "anomaly rules need a metric"
		}
		not("only for threshold rules", "op", "threshold", "sql", "query", "promql", "metric_query", "clear_threshold")
		not("only for SLO rules", "target")
	case "slo":
		if rc.metric != "" && rc.metric != "slo" {
			out["metric"] = "SLO rules have no metric"
		}
		not("only for threshold rules", "op", "threshold", "sql", "query", "promql", "metric_query", "clear_threshold")
		not("only for threshold and anomaly rules", "window", "sensitivity", "direction")
		if rc.instanceInScope || rc.checkInScope {
			out["scope"] = "SLO rules can't be scoped to an instance or check"
		}
	}
	return out
}

func (r *alertRuleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	rc, diags := readRuleCheck(ctx, req.Config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	names := make([]string, 0)
	probs := rc.problems()
	for n := range probs {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		resp.Diagnostics.AddAttributeError(path.Root(n), "Invalid alert rule", probs[n])
	}
}

func (m *ruleModel) input(ctx context.Context, diags *diag.Diagnostics) ruleInput {
	sp := apiSpec{
		Metric: m.Metric.ValueString(), SQL: m.SQL.ValueString(), Query: m.Query.ValueString(), PromQL: m.PromQL.ValueString(),
		Op: m.Op.ValueString(), Threshold: m.Threshold.ValueFloat64(), Window: m.Window.ValueString(), For: m.For.ValueString(),
		Repeat: m.Repeat.ValueString(), Sensitivity: m.Sensitivity.ValueString(), Direction: m.Direction.ValueString(),
		Target: m.Target.ValueFloat64(), NoData: m.NoData.ValueString(), ClearFor: m.ClearFor.ValueString(),
		Mention: m.Mention.ValueString(), Title: m.Title.ValueString(), Message: m.Message.ValueString(),
		Labels: mapOf(ctx, m.Labels, diags), DependsOn: idsOf(ctx, m.DependsOn, diags),
	}
	if !m.ClearThreshold.IsNull() && !m.ClearThreshold.IsUnknown() {
		v := m.ClearThreshold.ValueFloat64()
		sp.ClearThreshold = &v
	}
	if !m.NotifyResolved.ValueBool() {
		f := false
		sp.NotifyResolved = &f
	}
	if mq := m.MetricQuery; mq != nil {
		sp.MetricQuery = &apiMetricQuery{Name: mq.Name.ValueString(), Agg: mq.Agg.ValueString(), Group: mq.Group.ValueString(), Labels: mapOf(ctx, mq.Labels, diags)}
	}
	if sc := m.Scope; sc != nil {
		sp.Scope = apiScope{Route: sc.Route.ValueString(), Method: sc.Method.ValueString(), Instance: sc.Instance.ValueString(),
			Service: sc.Service.ValueString(), Check: sc.Check.ValueString(), EventKind: sc.EventKind.ValueString(),
			EventSeverity: sc.EventSeverity.ValueString(), Source: sc.Source.ValueString(), Host: sc.Host.ValueString(),
			Container: sc.Container.ValueString(), App: sc.App.ValueString()}
	}
	for _, st := range m.Escalation {
		sp.Escalation = append(sp.Escalation, apiStep{After: st.After.ValueString(), Channels: idsOf(ctx, st.Channels, diags)})
	}
	chans := idsOf(ctx, m.Channels, diags)
	if chans == nil {
		chans = []int64{}
	}
	return ruleInput{Name: m.Name.ValueString(), Kind: m.Kind.ValueString(), Severity: m.Severity.ValueString(), Channels: chans,
		Runbook: m.Runbook.ValueString(), Enabled: m.Enabled.ValueBool(), Spec: sp}
}

func (m *ruleModel) fill(r apiRule) {
	sp := r.Spec
	m.ID, m.Name, m.Kind, m.Severity = idString(r.ID), types.StringValue(r.Name), types.StringValue(r.Kind), types.StringValue(r.Severity)
	m.Channels, m.Runbook, m.Enabled = optIDList(r.Channels, m.Channels), optString(r.Runbook, m.Runbook), types.BoolValue(r.Enabled)
	m.Metric, m.SQL, m.Query, m.PromQL = types.StringValue(sp.Metric), optString(sp.SQL, m.SQL), optString(sp.Query, m.Query), optString(sp.PromQL, m.PromQL)
	m.Op, m.Threshold, m.Window = optString(sp.Op, m.Op), optFloat(sp.Threshold, m.Threshold), types.StringValue(sp.Window)
	m.For, m.Repeat = optString(sp.For, m.For), optString(sp.Repeat, m.Repeat)
	m.Sensitivity, m.Direction, m.Target = types.StringValue(sp.Sensitivity), optString(sp.Direction, m.Direction), types.Float64Value(sp.Target)
	// The server stores "ok", the default, as nothing.
	if !(sp.NoData == "" && m.NoData.ValueString() == "ok") {
		m.NoData = optString(sp.NoData, m.NoData)
	}
	m.ClearFor = optString(sp.ClearFor, m.ClearFor)
	m.ClearThreshold = types.Float64Null()
	if sp.ClearThreshold != nil {
		m.ClearThreshold = types.Float64Value(*sp.ClearThreshold)
	}
	m.NotifyResolved = types.BoolValue(sp.NotifyResolved == nil || *sp.NotifyResolved)
	m.Mention, m.Title, m.Message = optString(sp.Mention, m.Mention), optString(sp.Title, m.Title), optString(sp.Message, m.Message)
	m.Labels, m.DependsOn = optStringMap(sp.Labels, m.Labels), optIDList(sp.DependsOn, m.DependsOn)

	if sp.MetricQuery == nil {
		m.MetricQuery = nil
	} else {
		prev := m.MetricQuery
		if prev == nil {
			prev = &metricQueryModel{Agg: types.StringNull(), Group: types.StringNull(), Labels: types.MapNull(types.StringType)}
		}
		q := sp.MetricQuery
		m.MetricQuery = &metricQueryModel{Name: types.StringValue(q.Name), Agg: optString(q.Agg, prev.Agg), Group: optString(q.Group, prev.Group),
			Labels: optStringMap(q.Labels, prev.Labels)}
	}

	if sp.Scope == (apiScope{}) && m.Scope == nil {
		m.Scope = nil
	} else {
		prev := m.Scope
		if prev == nil {
			prev = &scopeModel{}
			*prev = nullScope()
		}
		sc := sp.Scope
		m.Scope = &scopeModel{Route: optString(sc.Route, prev.Route), Method: optString(sc.Method, prev.Method),
			Instance: optString(sc.Instance, prev.Instance), Service: optString(sc.Service, prev.Service), Check: optString(sc.Check, prev.Check),
			EventKind: optString(sc.EventKind, prev.EventKind), EventSeverity: optString(sc.EventSeverity, prev.EventSeverity),
			Source: optString(sc.Source, prev.Source), Host: optString(sc.Host, prev.Host), Container: optString(sc.Container, prev.Container),
			App: optString(sc.App, prev.App)}
	}

	if len(sp.Escalation) == 0 && m.Escalation == nil {
		m.Escalation = nil
	} else {
		m.Escalation = make([]escalationModel, len(sp.Escalation))
		for i, st := range sp.Escalation {
			m.Escalation[i] = escalationModel{After: types.StringValue(st.After), Channels: optIDList(st.Channels, types.ListUnknown(types.StringType))}
		}
	}
}

func nullScope() scopeModel {
	n := types.StringNull()
	return scopeModel{Route: n, Method: n, Instance: n, Service: n, Check: n, EventKind: n, EventSeverity: n, Source: n, Host: n, Container: n, App: n}
}

func (r *alertRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ruleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.input(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiRule
	if err := r.c.do(ctx, http.MethodPost, "/api/alerts/rules", in, &out); err != nil {
		apiDiag(&resp.Diagnostics, "create alert rule", err)
		return
	}
	plan.fill(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *alertRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ruleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiRule
	err := r.c.do(ctx, http.MethodGet, "/api/alerts/rules/"+state.ID.ValueString(), nil, &out)
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiag(&resp.Diagnostics, "read alert rule", err)
		return
	}
	state.fill(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *alertRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior ruleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.input(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiRule
	if err := r.c.do(ctx, http.MethodPut, "/api/alerts/rules/"+prior.ID.ValueString(), in, &out); err != nil {
		apiDiag(&resp.Diagnostics, "update alert rule", err)
		return
	}
	plan.fill(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *alertRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ruleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.c.do(ctx, http.MethodDelete, "/api/alerts/rules/"+state.ID.ValueString(), nil, nil)
	if err != nil && !isNotFound(err) {
		apiDiag(&resp.Diagnostics, "delete alert rule", err)
	}
}
