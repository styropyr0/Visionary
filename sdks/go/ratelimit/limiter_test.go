package ratelimit

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type caseStep struct {
	T        float64           `json:"t"`
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	RemoteIP string            `json:"remote_ip"`
	Headers  map[string]string `json:"headers"`
	Query    map[string]string `json:"query"`
	User     *struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	} `json:"user"`
	Attrs  map[string]string `json:"attrs"`
	Expect struct {
		Status      int     `json:"status"`
		Rule        *string `json:"rule"`
		RetryAfter  *int    `json:"retry_after"`
		Body        *string `json:"body"`
		ContentType *string `json:"content_type"`
	} `json:"expect"`
	Records []string `json:"records"`
}

type testCase struct {
	Name    string          `json:"name"`
	Service string          `json:"service"`
	Config  json.RawMessage `json:"config"`
	Steps   []caseStep      `json:"steps"`
}

func at(t float64) time.Time {
	sec, frac := math.Modf(t)
	return time.Unix(int64(sec), int64(math.Round(frac*1e9)))
}

func request(s caseStep) *Request {
	r := &Request{Method: s.Method, Path: s.Path, RemoteIP: s.RemoteIP}
	if r.Method == "" {
		r.Method = "GET"
	}
	if r.Path == "" {
		r.Path = "/"
	}
	if r.RemoteIP == "" {
		r.RemoteIP = "203.0.113.1"
	}
	r.Header = func(name string) string {
		for k, v := range s.Headers {
			if strings.EqualFold(k, name) {
				return v
			}
		}
		return ""
	}
	r.Query = func(name string) string { return s.Query[name] }
	return r
}

// TestSharedCases runs testdata/cases.json, which every SDK runs too.
func TestSharedCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []testCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			cfg, err := Parse(c.Config)
			if err != nil {
				t.Fatal(err)
			}
			l := New(c.Service)
			var records []string
			l.trace = func(id, outcome string) { records = append(records, id+":"+outcome) }
			l.Load(cfg)
			for i, s := range c.Steps {
				records = nil
				req := request(s)
				st := l.CheckRequest(req, at(s.T))
				var user User
				if s.User != nil {
					user = User{ID: s.User.ID, Role: s.User.Role}
				}
				l.CheckUser(st, req, user, s.Attrs, at(s.T))
				d := st.Denied()
				status := 0
				if d != nil {
					status = d.Status
				}
				if status != s.Expect.Status {
					t.Fatalf("step %d: status %d, want %d", i, status, s.Expect.Status)
				}
				if d != nil {
					if s.Expect.Rule != nil && d.RuleID != *s.Expect.Rule {
						t.Errorf("step %d: rule %s, want %s", i, d.RuleID, *s.Expect.Rule)
					}
					if s.Expect.RetryAfter != nil && d.RetryAfter != *s.Expect.RetryAfter {
						t.Errorf("step %d: retry after %d, want %d", i, d.RetryAfter, *s.Expect.RetryAfter)
					}
					if s.Expect.Body != nil && string(d.Body) != *s.Expect.Body {
						t.Errorf("step %d: body %q, want %q", i, d.Body, *s.Expect.Body)
					}
					if s.Expect.ContentType != nil && d.ContentType != *s.Expect.ContentType {
						t.Errorf("step %d: content type %q, want %q", i, d.ContentType, *s.Expect.ContentType)
					}
				}
				if len(records) == 0 && len(s.Records) == 0 {
					continue
				}
				if !reflect.DeepEqual(records, s.Records) {
					t.Errorf("step %d: records %v, want %v", i, records, s.Records)
				}
			}
		})
	}
}

func TestUserCheckRunsOnce(t *testing.T) {
	l := New("")
	l.Load(Config{Rules: []Rule{{ID: "u", Action: "limit", Key: "user", Limit: 1, WindowS: 60}}})
	req := &Request{Method: "GET", Path: "/", RemoteIP: "1.1.1.1"}
	now := at(6000)
	st := l.CheckRequest(req, now)
	if l.CheckUser(st, req, User{ID: "a"}, nil, now) != nil {
		t.Fatal("first request limited")
	}
	if l.CheckUser(st, req, User{ID: "a"}, nil, now) != nil {
		t.Fatal("a second check on the same request counted again")
	}
	st = l.CheckRequest(req, now)
	if l.CheckUser(st, req, User{ID: "a"}, nil, now) == nil {
		t.Fatal("second request passed")
	}
}

func TestCountersSurviveReloadUnlessShapeChanges(t *testing.T) {
	l := New("")
	rule := Rule{ID: "r", Action: "limit", Limit: 1, WindowS: 60, Algorithm: "fixed"}
	l.Load(Config{Rules: []Rule{rule}})
	req := &Request{Method: "GET", Path: "/", RemoteIP: "1.1.1.1"}
	now := at(6000)
	l.CheckRequest(req, now)
	rule.Name = "renamed"
	l.Load(Config{Rules: []Rule{rule}})
	if l.CheckRequest(req, now).Denied() == nil {
		t.Fatal("counter lost on a rename")
	}
	rule.WindowS = 30
	l.Load(Config{Rules: []Rule{rule}})
	if l.CheckRequest(req, now).Denied() != nil {
		t.Fatal("counter kept after the window changed")
	}
}

func TestRecordsAggregateAndRedact(t *testing.T) {
	l := New("api")
	l.Load(Config{Rules: []Rule{
		{ID: "k", Name: "Keys", Action: "limit", Key: "header:X-Api-Key", Limit: 0, WindowS: 60},
	}})
	req := &Request{Method: "post", Path: "/v1/x", RemoteIP: "1.1.1.1", Header: func(n string) string {
		if n == "x-api-key" {
			return "secret-value"
		}
		return ""
	}}
	for range 3 {
		l.CheckRequest(req, at(6000))
	}
	ev := l.Drain()
	if len(ev) != 1 || ev[0].Hits != 3 || ev[0].Outcome != Limited || ev[0].Method != "POST" || ev[0].RuleName != "Keys" {
		t.Fatalf("events %+v", ev)
	}
	if !strings.HasPrefix(ev[0].KeyValue, "sha256:") || len(ev[0].KeyValue) != 19 || strings.Contains(ev[0].KeyValue, "secret") {
		t.Fatalf("key value %q", ev[0].KeyValue)
	}
	if len(l.Drain()) != 0 {
		t.Fatal("drain kept events")
	}
}

func TestAggregatesOverflowIntoOther(t *testing.T) {
	l := New("")
	l.Load(Config{Rules: []Rule{{ID: "b", Action: "block"}}})
	for i := range MaxAggregates + 5 {
		ip := "10.0." + itoa(i/250) + "." + itoa(i%250)
		l.CheckRequest(&Request{Method: "GET", Path: "/", RemoteIP: ip}, at(1))
	}
	ev := l.Drain()
	if len(ev) != MaxAggregates+1 {
		t.Fatalf("%d aggregates", len(ev))
	}
	last := ev[len(ev)-1]
	if last.KeyValue != "(other)" || last.IP != "" || last.Hits != 5 {
		t.Fatalf("overflow %+v", last)
	}
}

func TestCounterCapDropsIdleThenAll(t *testing.T) {
	l := New("")
	l.Load(Config{Rules: []Rule{{ID: "c", Action: "limit", Limit: 5, WindowS: 1, Algorithm: "fixed"}}})
	for i := range MaxCounters {
		l.CheckRequest(&Request{Method: "GET", Path: "/", RemoteIP: "k" + itoa(i)}, at(100))
	}
	r := l.set.Load().byID["c"]
	if n := len(r.counters.m); n != MaxCounters {
		t.Fatalf("%d counters", n)
	}
	l.CheckRequest(&Request{Method: "GET", Path: "/", RemoteIP: "new"}, at(105))
	if n := len(r.counters.m); n != 1 {
		t.Fatalf("idle counters kept: %d", n)
	}
}

func TestValidate(t *testing.T) {
	ok := Config{Rules: []Rule{{ID: "a", Action: "limit", Limit: 10, WindowS: 60, Key: "attr:plan", Paths: []string{"/api/*"}, IPs: []string{"10.0.0.0/8"}}}}
	if err := Validate(ok); err != nil {
		t.Fatal(err)
	}
	bad := []Rule{
		{ID: "", Action: "block"},
		{ID: "a", Action: "throttle"},
		{ID: "a", Action: "limit", Limit: 1},
		{ID: "a", Action: "limit", Limit: 1.5, WindowS: 60},
		{ID: "a", Action: "block", IPs: []string{"nope"}},
		{ID: "a", Action: "block", Countries: []string{"USA"}},
		{ID: "a", Action: "block", Status: 200},
		{ID: "a", Action: "block", Paths: []string{"api"}},
		{ID: "a", Action: "block", Match: map[string][]string{"cookie": nil}},
	}
	for _, r := range bad {
		if Validate(Config{Rules: []Rule{r}}) == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	if Validate(Config{Rules: []Rule{{ID: "a", Action: "block"}, {ID: "a", Action: "allow"}}}) == nil {
		t.Error("accepted a duplicate id")
	}
}

func TestParseKeepsGoodRules(t *testing.T) {
	c, err := Parse([]byte(`{"rules":[{"id":"a","action":"block","limit":"x"},{"id":"b","action":"block"}]}`))
	if err != nil || len(c.Rules) != 1 || c.Rules[0].ID != "b" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := Parse([]byte(`{"rules":`)); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestGlob(t *testing.T) {
	for _, c := range []struct {
		p, s string
		want bool
	}{
		{"/a*", "/a", true}, {"/a*", "/a/b/c", true}, {"*", "/", true}, {"/a/*/c", "/a/b/c", true},
		{"/a/*/c", "/a/b/d", false}, {"/a", "/a/", false}, {"*.json", "/x/y.json", true}, {"/a**b", "/ab", true},
	} {
		if Glob(c.p, c.s) != c.want {
			t.Errorf("Glob(%q, %q) != %v", c.p, c.s, c.want)
		}
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
