package analytics

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/styropyr0/visionary/internal/agentapi"
	"github.com/styropyr0/visionary/sdks/go/ratelimit"
)

func blockCall(t *testing.T, s *Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "198.51.100.7:5000"
	r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Name: "Ed", Role: "admin"}))
	if id := strings.TrimPrefix(path, "/api/security/blocks/"); id != path {
		r.SetPathValue("id", id)
	}
	w := httptest.NewRecorder()
	switch method {
	case "POST":
		s.handleCreateBlock(w, r)
	case "DELETE":
		s.handleDeleteBlock(w, r)
	default:
		s.handleListBlocks(w, r)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func agentPoll(t *testing.T, s *Server, poll agentapi.BlocksPoll) agentapi.Blocks {
	t.Helper()
	body, _ := json.Marshal(poll)
	r := httptest.NewRequest("POST", agentapi.BlocksPath, strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer k1")
	w := httptest.NewRecorder()
	s.handleAgentBlocks(w, r)
	if w.Code != 200 {
		t.Fatalf("poll: %d %s", w.Code, w.Body.String())
	}
	var out agentapi.Blocks
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

func appBlockIPs(t *testing.T, s *Server) []string {
	t.Helper()
	cfg, _, _, err := s.loadRateLimits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ips []string
	for i, rule := range cfg.Rules {
		if isBlockRule(rule.ID) {
			if i != 0 && !isBlockRule(cfg.Rules[i-1].ID) || rule.Action != "block" || len(rule.IPs) == 0 {
				t.Fatalf("managed rule out of place or empty: %+v", cfg.Rules)
			}
			ips = append(ips, rule.IPs...)
		}
	}
	return ips
}

func TestBlocks(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	raw, _ := json.Marshal(ratelimit.Config{Rules: []ratelimit.Rule{{ID: "allowall", Action: "allow", IPs: []string{"203.0.113.0/24"}}}})
	if err := s.writeSettings(ctx, map[string]string{ratelimit.SettingConfig: string(raw)}); err != nil {
		t.Fatal(err)
	}

	for addr, want := range map[string]string{
		"10.0.0.5": "private", "127.0.0.1": "private", "100.64.1.1": "private", "8.0.0.0/7": "too wide",
		"198.51.100.0/24": "your own", "nope": "IP address",
	} {
		if code, out := blockCall(t, s, "POST", "/api/security/blocks", `{"address":"`+addr+`"}`); code != 400 || !strings.Contains(strings.ToLower(toString(out["error"])), strings.ToLower(want)) {
			t.Fatalf("%s: %d %v", addr, code, out)
		}
	}

	if code, out := blockCall(t, s, "POST", "/api/security/blocks", `{"address":"203.0.113.9","reason":"brute force"}`); code != 200 || out["address"] != "203.0.113.9" {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := blockCall(t, s, "POST", "/api/security/blocks", `{"address":"2001:db8:1::/48","app":false,"hosts":["web-1"]}`); code != 200 {
		t.Fatal(code)
	}
	if got := appBlockIPs(t, s); len(got) != 1 || got[0] != "203.0.113.9" {
		t.Fatalf("app rule: %v", got)
	}

	web := agentPoll(t, s, agentapi.BlocksPoll{Host: "web-1", Backend: "nftables"})
	db := agentPoll(t, s, agentapi.BlocksPoll{Host: "db-1", Backend: "nftables"})
	if len(web.Addresses) != 2 || len(db.Addresses) != 1 || db.Addresses[0] != "203.0.113.9" || web.Version == db.Version {
		t.Fatalf("web %+v db %+v", web, db)
	}
	agentPoll(t, s, agentapi.BlocksPoll{Host: "web-1", Backend: "nftables", Version: web.Version, Applied: 2})
	_, list := blockCall(t, s, "GET", "/api/security/blocks", "")
	hosts, _ := list["hosts"].([]any)
	current := map[string]bool{}
	for _, h := range hosts {
		m := h.(map[string]any)
		current[toString(m["host"])] = m["current"] == true
	}
	if !current["web-1"] || current["db-1"] {
		t.Fatalf("hosts: %v", hosts)
	}

	// Saving the Rate limits page keeps the managed rule it never shows
	r := httptest.NewRequest("PUT", "/api/ratelimits", strings.NewReader(`{"rules":[{"action":"allow","ips":["192.0.2.1"]}]}`))
	r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Role: "admin"}))
	w := httptest.NewRecorder()
	s.handlePutRateLimits(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), blockRulePrefix) || !strings.Contains(w.Body.String(), `"app_blocks":1`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if got := appBlockIPs(t, s); len(got) != 1 {
		t.Fatalf("managed rule lost on save: %v", got)
	}

	for _, b := range mustBlocks(t, s) {
		if code, _ := blockCall(t, s, "DELETE", "/api/security/blocks/"+toString(b.ID), ""); code != 200 {
			t.Fatal(code)
		}
	}
	if got := appBlockIPs(t, s); len(got) != 0 {
		t.Fatalf("an empty block list must leave no managed rule: %v", got)
	}
	if web := agentPoll(t, s, agentapi.BlocksPoll{Host: "web-1"}); len(web.Addresses) != 0 {
		t.Fatalf("%+v", web)
	}
}

func mustBlocks(t *testing.T, s *Server) []block {
	t.Helper()
	list, err := s.activeBlocks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestWithBlockRulesChunks(t *testing.T) {
	ips := make([]string, ratelimit.MaxListEntries+5)
	for i := range ips {
		ips[i] = "203.0.113.1"
	}
	rules := withBlockRules([]ratelimit.Rule{{ID: blockRulePrefix + "9"}, {ID: "mine", Action: "allow"}}, ips)
	if len(rules) != 3 || len(rules[0].IPs) != ratelimit.MaxListEntries || len(rules[1].IPs) != 5 || rules[2].ID != "mine" {
		t.Fatalf("%d rules", len(rules))
	}
	if got := withBlockRules(rules, nil); len(got) != 1 || got[0].ID != "mine" {
		t.Fatalf("%+v", got)
	}
}
