package analytics

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"testing"
	"time"
)

type memSource []*rawSeries

func (m memSource) load(_ context.Context, vs *vectorSel, w promWindow) ([]*rawSeries, error) {
	var out []*rawSeries
	for _, rs := range m {
		ok := true
		for _, mt := range vs.matchers {
			if !mt.matches(rs.labels[mt.name]) {
				ok = false
			}
		}
		if !ok {
			continue
		}
		cp := &rawSeries{labels: rs.labels.without(), counter: rs.counter}
		for _, s := range rs.samples {
			if s.T > w.from && s.T <= w.to {
				cp.samples = append(cp.samples, s)
			}
		}
		out = append(out, cp)
	}
	return out, nil
}

// t0 is a multiple of 300 s, so every test resolution lines up with it
const t0 = int64(1699999800) * 1000

func sec(n int64) int64 { return t0 + n*1000 }

// promFixture: two request counters (+1 and +2 every 15 s), three room
// gauges, and a latency histogram whose buckets grow 5, 9 and 10 every 15 s
func promFixture() memSource {
	var src memSource
	series := func(counter bool, l promLabels, v func(i int) float64) {
		rs := &rawSeries{labels: l, counter: counter}
		for i := 1; i <= 40; i++ {
			x := v(i)
			rs.samples = append(rs.samples, rawSample{T: sec(int64(15 * i)), V: x, Min: x - 1, Max: x + 1, N: 1})
		}
		src = append(src, rs)
	}
	series(true, promLabels{"__name__": "req_total", "svc": "a", "code": "200"}, func(int) float64 { return 1 })
	series(true, promLabels{"__name__": "req_total", "svc": "a", "code": "500"}, func(int) float64 { return 2 })
	series(false, promLabels{"__name__": "temp", "room": "a"}, func(i int) float64 { return float64(10 * i) })
	series(false, promLabels{"__name__": "temp", "room": "b"}, func(int) float64 { return 20 })
	series(false, promLabels{"__name__": "temp", "room": "c"}, func(int) float64 { return -40.5 })
	for le, n := range map[string]float64{"10": 5, "100": 9, "+Inf": 10} {
		series(true, promLabels{"__name__": "lat_bucket", "le": le}, func(int) float64 { return n })
	}
	return src
}

func evalAt(t *testing.T, src promSource, q string, at int64) promVal {
	t.Helper()
	expr, err := parsePromQL(q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	ev := newPromEval(context.Background(), src, expr, at, at, 0)
	v, err := ev.eval(expr)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func instantValues(v promVal) map[string]float64 {
	out := map[string]float64{}
	for _, s := range mergeSeries(v.vec) {
		if s.ok[0] {
			out[s.labels.String()] = s.v[0]
		}
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 || math.IsNaN(a) && math.IsNaN(b) }

func TestPromQLInstant(t *testing.T) {
	src := promFixture()
	at := sec(600)
	cases := []struct {
		q    string
		want map[string]float64
	}{
		{`req_total{code="200"}`, map[string]float64{`req_total{code="200", svc="a"}`: 20}},
		{`rate(req_total{code="200"}[1m])`, map[string]float64{`{code="200", svc="a"}`: 4.0 / 60}},
		{`increase(req_total{code="200"}[5m])`, map[string]float64{`{code="200", svc="a"}`: 20}},
		{`sum(rate(req_total[1m]))`, map[string]float64{`{}`: 12.0 / 60}},
		{`sum by (code) (increase(req_total[2m]))`, map[string]float64{`{code="200"}`: 8, `{code="500"}`: 16}},
		{`sum(increase(req_total[2m])) without (code)`, map[string]float64{`{svc="a"}`: 24}},
		{`irate(req_total{code="500"}[1m])`, map[string]float64{`{code="500", svc="a"}`: 2.0 / 15}},
		{`temp{room="a"}`, map[string]float64{`temp{room="a"}`: 400}},
		{`temp{room="a"} offset 1m`, map[string]float64{`temp{room="a"}`: 360}},
		{`avg_over_time(temp{room="a"}[1m])`, map[string]float64{`{room="a"}`: (370 + 380 + 390 + 400) / 4.0}},
		{`max_over_time(temp{room="a"}[1m])`, map[string]float64{`{room="a"}`: 401}},
		{`min_over_time(temp{room="a"}[1m])`, map[string]float64{`{room="a"}`: 369}},
		{`sum_over_time(temp{room="b"}[1m])`, map[string]float64{`{room="b"}`: 80}},
		{`count_over_time(temp{room="b"}[1m])`, map[string]float64{`{room="b"}`: 4}},
		{`last_over_time(temp{room="a"}[1m])`, map[string]float64{`temp{room="a"}`: 400}},
		{`delta(temp{room="a"}[1m])`, map[string]float64{`{room="a"}`: 30}},
		{`irate(temp{room="a"}[1m])`, map[string]float64{`{room="a"}`: 10.0 / 15}},
		{`avg(temp)`, map[string]float64{`{}`: (400 + 20 - 40.5) / 3}},
		{`min(temp)`, map[string]float64{`{}`: -40.5}},
		{`max(temp)`, map[string]float64{`{}`: 400}},
		{`count(temp)`, map[string]float64{`{}`: 3}},
		{`quantile(0.5, temp)`, map[string]float64{`{}`: 20}},
		{`quantile(0.75, temp)`, map[string]float64{`{}`: 210}},
		{`topk(1, temp)`, map[string]float64{`temp{room="a"}`: 400}},
		{`bottomk(2, temp)`, map[string]float64{`temp{room="c"}`: -40.5, `temp{room="b"}`: 20}},
		{`abs(temp{room="c"})`, map[string]float64{`{room="c"}`: 40.5}},
		{`ceil(temp{room="c"})`, map[string]float64{`{room="c"}`: -40}},
		{`floor(temp{room="c"})`, map[string]float64{`{room="c"}`: -41}},
		{`round(temp{room="c"})`, map[string]float64{`{room="c"}`: -40}},
		{`round(temp{room="a"} / 3, 5)`, map[string]float64{`{room="a"}`: 135}},
		{`clamp_min(temp, 0)`, map[string]float64{`{room="a"}`: 400, `{room="b"}`: 20, `{room="c"}`: 0}},
		{`clamp_max(temp, 100)`, map[string]float64{`{room="a"}`: 100, `{room="b"}`: 20, `{room="c"}`: -40.5}},
		{`temp > 0`, map[string]float64{`temp{room="a"}`: 400, `temp{room="b"}`: 20}},
		{`temp > bool 0`, map[string]float64{`{room="a"}`: 1, `{room="b"}`: 1, `{room="c"}`: 0}},
		{`100 < temp`, map[string]float64{`temp{room="a"}`: 400}},
		{`temp{room=~"a|b"} * 2`, map[string]float64{`{room="a"}`: 800, `{room="b"}`: 40}},
		{`temp{room!~"a|b"}`, map[string]float64{`temp{room="c"}`: -40.5}},
		{`temp{room!="a"} - 1`, map[string]float64{`{room="b"}`: 19, `{room="c"}`: -41.5}},
		{`-temp{room="b"}`, map[string]float64{`{room="b"}`: -20}},
		{`rate(req_total{code="200"}[1m]) + on(svc) rate(req_total{code="500"}[1m])`, map[string]float64{`{svc="a"}`: 12.0 / 60}},
		{`rate(req_total{code="500"}[1m]) / ignoring(code) rate(req_total{code="200"}[1m])`, map[string]float64{`{svc="a"}`: 2}},
		{`increase(req_total[1m]) / on(svc) group_left sum by (svc) (increase(req_total[1m]))`,
			map[string]float64{`{code="200", svc="a"}`: 1.0 / 3, `{code="500", svc="a"}`: 2.0 / 3}},
		{`sum by (svc) (increase(req_total[1m])) / on(svc) group_right increase(req_total[1m])`,
			map[string]float64{`{code="200", svc="a"}`: 3, `{code="500", svc="a"}`: 1.5}},
		{`histogram_quantile(0.5, rate(lat_bucket[1m]))`, map[string]float64{`{}`: 10}},
		{`histogram_quantile(0.7, sum by (le) (increase(lat_bucket[1m])))`, map[string]float64{`{}`: 55}},
		{`histogram_quantile(0.99, increase(lat_bucket[1m]))`, map[string]float64{`{}`: 100}},
		{`temp and temp > 0`, map[string]float64{`temp{room="a"}`: 400, `temp{room="b"}`: 20}},
		{`temp unless temp > 0`, map[string]float64{`temp{room="c"}`: -40.5}},
		{`temp{room="a"} or temp{room="b"}`, map[string]float64{`temp{room="a"}`: 400, `temp{room="b"}`: 20}},
		{`absent(nothing{job="x"})`, map[string]float64{`{job="x"}`: 1}},
		{`absent(temp)`, map[string]float64{}},
		{`vector(3) * 2`, map[string]float64{`{}`: 6}},
		{`temp{room="b"} ^ 2 / 4 % 7`, map[string]float64{`{room="b"}`: 100 % 7}},
	}
	for _, c := range cases {
		got := instantValues(evalAt(t, src, c.q, at))
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.q, got, c.want)
			continue
		}
		for k, w := range c.want {
			if g, ok := got[k]; !ok || !near(g, w) {
				t.Errorf("%s: %s = %v (present %v), want %v; all %v", c.q, k, g, ok, w, got)
			}
		}
	}
}

func TestPromQLScalars(t *testing.T) {
	for q, want := range map[string]float64{
		`1 + 2 * 3`:              7,
		`(1 + 2) * 3`:            9,
		`2 ^ 3 ^ 2`:              512,
		`-2 ^ 2`:                 -4,
		`10 % 4`:                 2,
		`1 < bool 2`:             1,
		`scalar(temp{room="b"})`: 20,
		`0x10 + 1e1`:             26,
	} {
		v := evalAt(t, promFixture(), q, sec(600))
		if !v.isScalar || !near(v.scalar[0], want) {
			t.Errorf("%s = %+v, want %v", q, v.scalar, want)
		}
	}
	if v := evalAt(t, promFixture(), `scalar(temp)`, sec(600)); !math.IsNaN(v.scalar[0]) {
		t.Errorf("scalar of three series = %v, want NaN", v.scalar[0])
	}
}

func TestPromQLRange(t *testing.T) {
	expr, err := parsePromQL(`sum(rate(req_total[1m]))`)
	if err != nil {
		t.Fatal(err)
	}
	ev := newPromEval(context.Background(), promFixture(), expr, sec(60), sec(900), 60_000)
	v, err := ev.eval(expr)
	if err != nil {
		t.Fatal(err)
	}
	if ev.n != 15 || len(v.vec) != 1 {
		t.Fatalf("steps %d, series %d", ev.n, len(v.vec))
	}
	for i := 0; i < ev.n; i++ {
		at := (ev.at(i) - t0) / 1000
		want, ok := 0.2, at <= 600
		if ok != v.vec[0].ok[i] || ok && !near(v.vec[0].v[i], want) {
			t.Errorf("at %ds: %v %v, want %v %v", at, v.vec[0].v[i], v.vec[0].ok[i], want, ok)
		}
	}
	expr, _ = parsePromQL(`temp[1m]`)
	if _, err := newPromEval(context.Background(), promFixture(), expr, sec(60), sec(120), 60_000).eval(expr); err == nil {
		t.Error("a range vector in a range query should fail")
	}
	m := evalAt(t, promFixture(), `temp{room="a"}[1m]`, sec(600))
	if len(m.matrix) != 1 || len(m.matrix[0].samples) != 4 || m.matrix[0].samples[3].V != 400 {
		t.Errorf("matrix: %+v", m.matrix)
	}
}

func TestPromQLResolution(t *testing.T) {
	expr, _ := parsePromQL(`rate(x[5m])`)
	ev := newPromEval(context.Background(), memSource{}, expr, sec(0), sec(3600), 60_000)
	if ev.res != 15 || ev.phase != 0 {
		t.Errorf("1h at 60s: res %d phase %d", ev.res, ev.phase)
	}
	ev = newPromEval(context.Background(), memSource{}, expr, sec(7), sec(30*86400+7), 3600_000)
	if 3600%ev.res != 0 || (30*86400+600)/ev.res > maxPromBuckets || ev.phase != (t0/1000+7)%ev.res {
		t.Errorf("30d at 1h: res %d phase %d", ev.res, ev.phase)
	}
}

func TestPromQLErrors(t *testing.T) {
	for q, want := range map[string]struct {
		pos int
		msg string
	}{
		`rate(req_total)`:          {5, "range vector"},
		`sum(req_total`:            {13, "expected , or )"},
		`req_total{code="200"`:     {20, "expected , or }"},
		`{code=""}`:                {0, "does not match the empty string"},
		`req_total{code=~"("}`:     {16, "regular expression"},
		`req_total[5]`:             {10, "duration"},
		`foo(bar)`:                 {0, "unknown"},
		`1 > 2`:                    {2, "need bool"},
		`req_total[5m:1m]`:         {12, "subqueries"},
		`sum(x) by (a) + `:         {16, "end of query"},
		`topk(x, y)`:               {5, "scalar"},
		`req_total{code="200"} 3`:  {22, "unexpected"},
		`"unterminated`:            {0, "unterminated"},
		`rate(x[5m]) and 1`:        {12, "instant vectors"},
		`histogram_quantile(x, y)`: {19, "scalar"},
	} {
		_, err := parsePromQL(q)
		var pe *promError
		if !errors.As(err, &pe) {
			t.Errorf("%s: %v, want a promError", q, err)
			continue
		}
		if pe.Pos != want.pos || !strings.Contains(pe.Msg, want.msg) {
			t.Errorf("%s: at %d %q, want at %d containing %q", q, pe.Pos, pe.Msg, want.pos, want.msg)
		}
	}
	expr, _ := parsePromQL(`temp + on() temp`)
	if _, err := newPromEval(context.Background(), promFixture(), expr, sec(600), sec(600), 0).eval(expr); err == nil || !strings.Contains(err.Error(), "many-to-many") {
		t.Errorf("many-to-many: %v", err)
	}
}

func TestPromQLParse(t *testing.T) {
	for _, q := range []string{
		`up`, `up{job="a", instance!="b"}`, `{__name__=~"req.*"}`, `rate(x[1h30m] offset 1d)`, `x offset -5m`,
		`sum without (a, b) (x)`, `count by () (x)`, `x # comment`, `x{a='single', b=` + "`raw`" + `}`,
		`sum(rate(x[5m])) by (le)`, `a / on(x) group_left(y) b`, `a == bool b`, `a atan2 b`,
	} {
		if _, err := parsePromQL(q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
	e, _ := parsePromQL(`rate(x{a="1"}[5m] offset 1h)`)
	m := e.(*callExpr).args[0].(*matrixSel)
	if m.rng != 5*time.Minute || m.vs.offset != time.Hour || m.vs.name != "x" || len(m.vs.matchers) != 2 {
		t.Errorf("parsed %+v %+v", m, m.vs)
	}
}

func TestPromQLOverSQL(t *testing.T) {
	s := metricsTestServer(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute)
	ins := func(at time.Time, name, kind, labels string, value float64, count any, sum any, buckets any) {
		t.Helper()
		err := s.db.Exec(ctx, `INSERT INTO metrics (created_at, service, instance, name, kind, unit, labels, series, value, count, sum, min, max, buckets)
VALUES (?, 'shop', 'web-1', ?, ?, NULL, ?, ?, ?, ?, ?, NULL, NULL, ?)`, at.Format(tsLayout), name, kind, labels, seriesHex(name+labels), value, count, sum, buckets)
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 8; i++ {
		at := now.Add(-time.Duration(i*15) * time.Second).Add(time.Second)
		ins(at, "orders_placed", "counter", `{"region":"eu"}`, 3, nil, nil, nil)
		ins(at, "orders_placed", "counter", `{"region":"us"}`, 1, nil, nil, nil)
		ins(at, "queue_depth", "gauge", `{}`, float64(i), nil, nil, nil)
		ins(at, "checkout_ms", "histogram", `{}`, 30, 10, 300.0, `{"bounds":[10,100],"counts":[2,6,2]}`)
		for j := 0; j < 2; j++ {
			status := 200
			if j == 1 {
				status = 500
			}
			err := s.db.Exec(ctx, `INSERT INTO api_logs (request_id, created_at, method, path, route, status, duration_ms, service) VALUES ('r', ?, 'GET', '/x', '/x', ?, ?, 'shop')`,
				at.Format(tsLayout), status, float64(20*(j+1)))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	run := func(q string) map[string]float64 {
		t.Helper()
		_, v, _, err := s.runPromQL(ctx, q, now, now, 0)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return instantValues(v)
	}
	expect := func(q string, want map[string]float64) {
		t.Helper()
		got := run(q)
		if len(got) != len(want) {
			t.Errorf("%s: got %v, want %v", q, got, want)
			return
		}
		for k, w := range want {
			if !near(got[k], w) {
				t.Errorf("%s: %s = %v, want %v (all %v)", q, k, got[k], w, got)
			}
		}
	}
	expect(`sum by (region) (increase(orders_placed[2m]))`, map[string]float64{`{region="eu"}`: 24, `{region="us"}`: 8})
	expect(`sum(rate(orders_placed{region="eu", service="shop"}[1m]))`, map[string]float64{`{}`: 12.0 / 60})
	expect(`max_over_time(queue_depth[5m])`, map[string]float64{`{instance="web-1", service="shop"}`: 8})
	expect(`queue_depth`, map[string]float64{`queue_depth{instance="web-1", service="shop"}`: 1})
	expect(`sum(increase(checkout_ms_count[5m]))`, map[string]float64{`{}`: 80})
	expect(`histogram_quantile(0.5, sum by (le) (increase(checkout_ms_bucket[5m])))`, map[string]float64{`{}`: 10 + 90*(40.0-16)/48})
	expect(`sum by (status) (increase(visionary_http_requests_total{service="shop"}[5m]))`, map[string]float64{`{status="200"}`: 8, `{status="500"}`: 8})
	expect(`sum(increase(visionary_http_request_duration_ms_bucket{le="25"}[5m]))`, map[string]float64{`{}`: 8})
	expect(`histogram_quantile(1, sum by (le) (rate(visionary_http_request_duration_ms_bucket[5m])))`, map[string]float64{`{}`: 50})

	cat, _, err := s.promCatalog(ctx, nil, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, l := range cat {
		names[l["__name__"]] = true
	}
	for _, n := range []string{"orders_placed", "checkout_ms_bucket", "checkout_ms_count", "visionary_http_requests_total", "visionary_http_request_duration_ms_bucket"} {
		if !names[n] {
			keys := make([]string, 0, len(names))
			for k := range names {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			t.Errorf("catalog lacks %s: %v", n, keys)
		}
	}
}

func seriesHex(s string) string {
	const hex = "0123456789abcdef"
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * 1099511628211
	}
	out := make([]byte, 16)
	for i := range out {
		out[i] = hex[(h>>(4*uint(i)))&15]
	}
	return string(out)
}

func TestPromChartWidensWindows(t *testing.T) {
	s := metricsTestServer(t)
	ctx := context.Background()
	to := time.Now().UTC().Truncate(30 * time.Minute)
	from := to.Add(-6 * time.Hour)
	for at := from.Add(time.Minute); at.Before(to); at = at.Add(20 * time.Minute) {
		if err := s.db.Exec(ctx, `INSERT INTO api_logs (request_id, created_at, method, path, route, status, duration_ms, service) VALUES ('r', ?, 'GET', '/x', '/x', 200, 5, 'shop')`,
			at.Format(tsLayout)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.promResult(ctx, `sum(rate(visionary_http_requests_total[5m]))`, timeRange{From: from, To: to, Bucket: 1800})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) < 11 {
		t.Fatalf("%d points over 12 buckets: %v", len(res.Rows), res.Rows)
	}
	for _, r := range res.Rows {
		if v := r[2].(float64); v <= 0 || v > 2.0/1800 {
			t.Fatalf("rate %v, want one or two requests per 30 minutes", v)
		}
	}
}
