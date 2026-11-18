package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/styropyr0/visionary/sdks/go/ratelimit"
)

func rateLimitRequest(method, target, body, role string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	return r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Role: role}))
}

func TestRateLimitsSaveAndValidate(t *testing.T) {
	s := securityTestServer(t, map[string]string{"security_route_limits": "POST /api/export* 10"})

	w := httptest.NewRecorder()
	s.handlePutRateLimits(w, rateLimitRequest("PUT", "/api/ratelimits", `{"rules":[]}`, "editor"))
	if w.Code != http.StatusForbidden {
		t.Fatal("an editor saved rate limits", w.Code)
	}

	w = httptest.NewRecorder()
	s.handlePutRateLimits(w, rateLimitRequest("PUT", "/api/ratelimits", `{"rules":[{"action":"limit","limit":1.5,"window_s":60}]}`, "admin"))
	if w.Code != http.StatusBadRequest {
		t.Fatal("accepted a fractional limit", w.Code, w.Body)
	}

	body := `{"enabled":true,"ip_header":" CF-Connecting-IP ","rules":[
		{"name":" Export ","action":"limit","methods":["post"," "],"paths":["/api/export*"],"limit":10,"window_s":60,"key":"ip","algorithm":"sliding","burst":5},
		{"action":"block","countries":["cn"],"limit":3}
	]}`
	w = httptest.NewRecorder()
	s.handlePutRateLimits(w, rateLimitRequest("PUT", "/api/ratelimits", body, "admin"))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	var v rateLimitsView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	rules := v.Config.Rules
	if len(rules) != 2 || rules[0].ID == "" || rules[0].ID == rules[1].ID || rules[0].Name != "Export" || v.Version == "" {
		t.Fatalf("saved %+v", v)
	}
	if len(rules[0].Methods) != 1 || rules[0].Methods[0] != "POST" || rules[0].Key != "" || rules[0].Algorithm != "" || rules[0].Burst != 0 {
		t.Fatalf("not normalized: %+v", rules[0])
	}
	if rules[1].Countries[0] != "CN" || rules[1].Limit != 0 || v.Config.IPHeader != "CF-Connecting-IP" || v.Config.Enabled != nil {
		t.Fatalf("not normalized: %+v %+v", rules[1], v.Config)
	}
	if len(v.RouteLimits) != 1 || v.RouteLimits[0].PerMin != 10 || v.RouteLimits[0].Method != "POST" {
		t.Fatalf("route limits %+v", v.RouteLimits)
	}

	// what the SDKs read back parses to the same rules
	res, err := s.db.Query(context.Background(), `SELECT value FROM visionary_settings WHERE name = ?`, ratelimit.SettingConfig)
	if err != nil || len(res.Rows) != 1 {
		t.Fatal(err)
	}
	parsed, err := ratelimit.Parse([]byte(toString(res.Rows[0][0])))
	if err != nil || len(parsed.Rules) != 2 || ratelimit.Validate(parsed) != nil {
		t.Fatalf("%+v %v", parsed, err)
	}

	first := v.Version
	w = httptest.NewRecorder()
	s.handlePutRateLimits(w, rateLimitRequest("PUT", "/api/ratelimits", body, "admin"))
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	if v.Version == first {
		t.Fatal("version unchanged after a save")
	}
}

func TestRateLimitActivityAndFindings(t *testing.T) {
	s := securityTestServer(t, map[string]string{"security_rate_limited_hits": "50"})
	ctx := context.Background()
	now := time.Now().UTC()
	at := now.Add(-2 * time.Minute).Format(tsLayout)
	for _, row := range [][]any{
		{at, "shop", "i1", "r1", "Export", "limited", "203.0.113.9", "203.0.113.9", "US", "POST", "/api/export", nil, 40},
		{at, "shop", "i2", "r1", "Export", "limited", "203.0.113.9", "203.0.113.9", "US", "POST", "/api/export", nil, 20},
		{at, "shop", "i1", "r2", "Block CN", "would_block", nil, "198.51.100.4", "CN", "GET", "/", nil, 7},
	} {
		if err := s.db.Exec(ctx, `INSERT INTO rate_limit_events (created_at, service, instance, rule_id, rule_name, outcome, key_value, ip, country, method, path, user_id, hits)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, row...); err != nil {
			t.Fatal(err)
		}
	}

	w := httptest.NewRecorder()
	q := "from=" + now.Add(-time.Hour).Format(time.RFC3339) + "&to=" + now.Add(time.Minute).Format(time.RFC3339)
	s.handleRateLimitActivity(w, rateLimitRequest("GET", "/api/ratelimits/activity?"+q, "", "viewer"))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	var act struct {
		Rules []struct {
			RuleID     string `json:"rule_id"`
			Limited    int64  `json:"limited"`
			WouldBlock int64  `json:"would_block"`
		} `json:"rules"`
		Clients []struct {
			IP       string `json:"ip"`
			Hits     int64  `json:"hits"`
			Enforced int64  `json:"enforced"`
		} `json:"clients"`
		Recent []map[string]any `json:"recent"`
		Series []map[string]any `json:"series"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &act); err != nil {
		t.Fatal(err)
	}
	if len(act.Rules) != 2 || act.Rules[0].RuleID != "r1" || act.Rules[0].Limited != 60 || act.Rules[1].WouldBlock != 7 {
		t.Fatalf("rules %+v", act.Rules)
	}
	if len(act.Clients) != 2 || act.Clients[0].Hits != 60 || act.Clients[1].Enforced != 0 || len(act.Recent) != 3 || len(act.Series) == 0 {
		t.Fatalf("%+v", act)
	}

	found, err := s.detectRateLimited(ctx, now.Add(-time.Hour), now, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].kind != "rate_limited" || found[0].ip != "203.0.113.9" || found[0].n != 60 {
		t.Fatalf("findings %+v", found)
	}
}
