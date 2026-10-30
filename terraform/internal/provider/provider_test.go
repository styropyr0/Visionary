package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestSchemasAreValid(t *testing.T) {
	ctx := context.Background()
	p := New("test")()
	var ps provider.SchemaResponse
	p.Schema(ctx, provider.SchemaRequest{}, &ps)
	if ps.Diagnostics.HasError() {
		t.Fatal(ps.Diagnostics)
	}
	for _, f := range p.Resources(ctx) {
		r := f()
		var meta resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "visionary"}, &meta)
		var s resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &s)
		if s.Diagnostics.HasError() {
			t.Fatalf("%s: %v", meta.TypeName, s.Diagnostics)
		}
		if diags := s.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Fatalf("%s: %v", meta.TypeName, diags)
		}
	}
	for _, f := range p.DataSources(ctx) {
		d := f()
		var meta datasource.MetadataResponse
		d.Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "visionary"}, &meta)
		var s datasource.SchemaResponse
		d.Schema(ctx, datasource.SchemaRequest{}, &s)
		if diags := s.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Fatalf("%s: %v", meta.TypeName, diags)
		}
	}
}

func TestSamePanels(t *testing.T) {
	a := `[{"id":"p1","kind":"builtin","ref":"overview/latency","x":0,"y":0,"w":6,"h":4,"viz":{"type":"bar","unit":"ms"}}]`
	b := `[ {"h":4, "w":6, "y":0, "x":0, "ref":"overview/latency", "kind":"builtin", "id":"p1", "title":"", "params":{},
	  "viz": {"unit":"ms", "type":"bar"}} ]`
	if !samePanels(a, b) {
		t.Fatal("reformatted panels should be equal")
	}
	if samePanels(a, `[{"id":"p1","kind":"builtin","ref":"overview/latency","x":0,"y":0,"w":12,"h":4}]`) {
		t.Fatal("a resized panel should differ")
	}
	for _, bad := range []string{`{}`, `null`, `[{"id":"a"}]`, `[{"id":"a","kind":"x","ref":"r"}]`, `[{"id":"a","kind":"saved","ref":"1","oops":1}]`} {
		if _, err := parsePanels(bad); err == nil {
			t.Errorf("%s should be refused", bad)
		}
	}
}

func TestRuleProblems(t *testing.T) {
	check := func(kind, metric string, set ...string) map[string]string {
		on := map[string]bool{}
		for _, s := range set {
			on[s] = true
		}
		return ruleCheck{kind: kind, metric: metric, set: func(n string) bool { return on[n] }, metricQuery: on["metric_query"], anyScope: on["scope"]}.problems()
	}
	if p := check("threshold", "latency_p95", "op", "threshold"); len(p) != 0 {
		t.Fatalf("valid rule refused: %v", p)
	}
	if p := check("threshold", "latency_p95"); p["op"] == "" {
		t.Fatal("op should be required")
	}
	if p := check("threshold", "custom", "op"); p["sql"] == "" {
		t.Fatal("custom needs sql")
	}
	if p := check("threshold", "promql", "op", "promql", "scope"); p["scope"] == "" {
		t.Fatal("promql takes no scope")
	}
	if p := check("anomaly", "rpm", "op", "target"); p["op"] == "" || p["target"] == "" {
		t.Fatalf("anomaly: %v", p)
	}
	if p := check("slo", "", "window"); p["window"] == "" {
		t.Fatal("slo has no window")
	}
	if p := check("threshold", "custom_metric", "op"); p["metric_query"] == "" {
		t.Fatal("custom_metric needs metric_query")
	}
}

func TestStateHelpers(t *testing.T) {
	if !optString("", types.StringNull()).IsNull() {
		t.Fatal("empty with no config should stay null")
	}
	if v := optString("", types.StringValue("x")); v.IsNull() || v.ValueString() != "" {
		t.Fatal("a value that went away should show as a change")
	}
	ref := types.MapValueMust(types.StringType, map[string]attr.Value{"Authorization": types.StringValue("Bearer x")})
	got := unmask(map[string]string{"Authorization": uptimeMask, "Accept": "a"}, ref)
	el := got.Elements()
	if el["Authorization"].(types.String).ValueString() != "Bearer x" || el["Accept"].(types.String).ValueString() != "a" {
		t.Fatalf("unmask: %v", el)
	}
	if v := sameTime("2030-01-10T21:00:00Z", types.StringValue("2030-01-10T22:00:00+01:00")); v.ValueString() != "2030-01-10T22:00:00+01:00" {
		t.Fatalf("same instant should keep the written form: %s", v)
	}
}

func TestClientErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer vis_k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"rule not found"}`))
	}))
	defer ts.Close()
	err := newClient(ts.URL+"/", "vis_k", "test").do(context.Background(), http.MethodGet, "/api/alerts/rules/9", nil, nil)
	if !isNotFound(err) || err.Error() != "Visionary answered 404: rule not found" {
		t.Fatalf("got %v", err)
	}
}
