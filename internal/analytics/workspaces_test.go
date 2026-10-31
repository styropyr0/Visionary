package analytics

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

func TestScopeRewrite(t *testing.T) {
	sc := &wsScope{d: sdk.SQLite, patterns: []string{"pay-*", "web"}, ids: []int64{3}}
	cases := []struct{ in, want string }{
		{`SELECT COUNT(*) FROM api_logs WHERE status >= 500`,
			`SELECT COUNT(*) FROM (SELECT * FROM api_logs WHERE (service GLOB 'pay-*' OR service IN ('web'))) api_logs WHERE status >= 500`},
		{`SELECT a.id FROM api_logs a JOIN spans AS s ON s.request_id = a.request_id`,
			`SELECT a.id FROM (SELECT * FROM api_logs WHERE (service GLOB 'pay-*' OR service IN ('web'))) a JOIN (SELECT * FROM spans WHERE (service GLOB 'pay-*' OR service IN ('web'))) AS s ON s.request_id = a.request_id`},
		{`SELECT * FROM dashboards`, `SELECT * FROM (SELECT * FROM dashboards WHERE (workspace_id IS NULL OR workspace_id IN (3))) dashboards`},
		{`SELECT x FROM visionary_settings`, `SELECT x FROM visionary_settings`},
		{`SELECT EXTRACT(EPOCH FROM created_at) FROM errors, json_each(errors.stack)`,
			`SELECT EXTRACT(EPOCH FROM created_at) FROM (SELECT * FROM errors WHERE (service GLOB 'pay-*' OR service IN ('web'))) errors, json_each(errors.stack)`},
	}
	for _, c := range cases {
		got, err := sc.rewrite(c.in, false)
		if err != nil || got != c.want {
			t.Errorf("rewrite(%s)\n got %s (%v)\nwant %s", c.in, got, err, c.want)
		}
	}
	// SQL from people may only name scoped tables, shared ones and real CTEs.
	for _, q := range []string{
		`SELECT * FROM visionary_users`,
		`SELECT * FROM main.api_logs`,
		`SELECT COUNT(*) OVER errs FROM api_logs WINDOW secret AS (ORDER BY id) UNION SELECT 1 FROM secret`,
	} {
		if _, err := sc.rewrite(q, true); err == nil {
			t.Errorf("untrusted %q was let through", q)
		}
	}
	if _, err := sc.rewrite(`WITH x AS (SELECT service FROM api_logs) SELECT * FROM x`, true); err != nil {
		t.Errorf("a CTE was refused: %v", err)
	}
	got, err := sc.restrictWrite(`DELETE FROM alert_rules WHERE id = ?`)
	if err != nil || got != `DELETE FROM alert_rules WHERE (id = ?) AND workspace_id IN (3) ` {
		t.Errorf("restrictWrite: %q %v", got, err)
	}
	got, _ = sc.restrictWrite(`UPDATE dashboards SET name = ? WHERE id = ? LIMIT 1`)
	if got != `UPDATE dashboards SET name = ? WHERE (id = ?) AND (workspace_id IS NULL OR workspace_id IN (3)) LIMIT 1` {
		t.Errorf("restrictWrite: %q", got)
	}
	if got := ownedInsert(`INSERT INTO log_views (name) VALUES (?)`); got != "log_views" {
		t.Errorf("ownedInsert: %q", got)
	}
	for _, d := range []sdk.Dialect{sdk.Postgres, sdk.MySQL} {
		sc := &wsScope{d: d, patterns: []string{"pay_*"}}
		p, _ := sc.pred("API_LOGS")
		if !strings.Contains(p, `LIKE 'pay!_%' ESCAPE '!'`) {
			t.Errorf("%v: %s", d, p)
		}
	}
}

func TestServicePatterns(t *testing.T) {
	for _, ok := range []string{"pay-api", "pay-*", "*-api", "*web*", "*", "team/svc@2"} {
		b := workspaceBody{Name: "x", Services: []string{ok}}
		if err := b.clean(); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"a*b", "x'y", `a"b`, "a,b", `a\b`, "**"} {
		b := workspaceBody{Name: "x", Services: []string{bad}}
		if err := b.clean(); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// ---------- The leak test ----------

// Every row of the hidden service carries this marker somewhere in its data,
// never in an id, so a response that contains it leaked a row.
const leak = "zzleak"

const (
	secretTrace = "e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0"
	payTrace    = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
)

type wsFixture struct {
	s                *Server
	h                http.Handler
	scoped, unscoped string // read API keys
	at               time.Time
}

func newWSFixture(t *testing.T) *wsFixture {
	t.Helper()
	s := newTestServer(t, "ws.db", nil)
	t.Cleanup(func() { _ = s.db.Close() })
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	f := &wsFixture{s: s, h: s.Handler(), at: time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)}
	exec := func(q string, args ...any) {
		t.Helper()
		if err := s.db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	ts := f.at.Format(tsLayout)
	// The hidden service first, so its rows have the low ids that direct
	// links below ask for.
	for _, svc := range []struct{ name, web, mark, trace, host, fp, num string }{
		{leak + "-svc", leak + "-web", leak, secretTrace, leak + "-host", "f00d0001", "1"},
		{"pay-api", "pay-web", "payok", payTrace, "pay-host", "f00d0002", "2"},
	} {
		m := svc.mark
		exec(`INSERT INTO api_logs (request_id, created_at, method, path, route, status, duration_ms, ip, user_agent, user_handle, service, version, login_account, trace_id, span_id, db_count, db_ms)
VALUES (?, ?, 'POST', ?, ?, 500, 1234, ?, ?, ?, ?, ?, ?, ?, ?, 3, 40)`, "req"+svc.num, ts, "/"+m+"/1", "/"+m+"/{id}", "10.0.0."+svc.num, m+"-agent", m+"-user", svc.name, m+"-v1", "acct"+svc.num, svc.trace, "b00000000000000"+svc.num)
		exec(`INSERT INTO spans (created_at, request_id, service, instance, kind, name, target, offset_ms, duration_ms, status, error, trace_id, span_id, parent_span_id)
VALUES (?, ?, ?, ?, 'db', ?, ?, 1, 900, 'error', ?, ?, ?, ?)`, ts, "req"+svc.num, svc.name, svc.host, "SELECT "+m, m+"-db", m+" failed", svc.trace, "c00000000000000"+svc.num, "b00000000000000"+svc.num)
		exec(`INSERT INTO spans (created_at, request_id, service, instance, kind, name, target, offset_ms, duration_ms, status)
VALUES (?, ?, ?, ?, 'http', ?, ?, 1, 50, 'ok')`, ts, "req"+svc.num, svc.name, svc.host, "GET "+m+".example", m+".example")
		exec(`INSERT INTO errors (created_at, service, instance, version, request_id, method, route, path, type, message, stack, fingerprint, handled)
VALUES (?, ?, ?, ?, ?, 'POST', ?, ?, ?, ?, ?, ?, 0)`, ts, svc.name, svc.host, m+"-v1", "req"+svc.num, "/"+m+"/{id}", "/"+m+"/1", m+"Error", m+" boom", m+".go:1", svc.fp)
		exec(`INSERT INTO releases (created_at, service, instance, version, sdk, runtime) VALUES (?, ?, ?, ?, 'go', 'go1')`, ts, svc.name, svc.host, m+"-v1")
		exec(`INSERT INTO system_metrics (created_at, instance, interval_s, host_cpu_percent, proc_rss_mb, goroutines, service, version, sdk, runtime) VALUES (?, ?, 15, 50, 100, 10, ?, ?, 'go', 'go1')`,
			ts, svc.host, svc.name, m+"-v1")
		exec(`INSERT INTO app_logs (created_at, service, instance, version, source, host, level, severity, logger, message, attrs, request_id, trace_id, pattern, size)
VALUES (?, ?, ?, ?, 'sdk', ?, 'error', 17, ?, ?, ?, ?, ?, ?, 10)`, ts, svc.name, svc.host, m+"-v1", svc.host, m+"-logger", m+" line failed", `{"k":"`+m+`"}`, "req"+svc.num, svc.trace, "p"+svc.num)
		exec(`INSERT INTO log_patterns (id, service, template, sample, level, severity, first_seen, last_seen, `+s.q.ident("lines")+`, bytes) VALUES (?, ?, ?, ?, 'error', 17, ?, ?, 1, 10)`,
			"p"+svc.num, svc.name, m+" line <*>", m+" line failed", ts, ts)
		exec(`INSERT INTO metrics (created_at, service, instance, name, kind, labels, series, value) VALUES (?, ?, ?, 'orders_total', 'counter', ?, ?, 5)`,
			ts, svc.name, svc.host, `{"tenant":"`+m+`"}`, "000000000000000"+svc.num)
		exec(`INSERT INTO profiles (created_at, service, instance, version, sdk, duration_ms, samples, routes, folded) VALUES (?, ?, ?, ?, 'go', 1000, 10, ?, ?)`,
			ts, svc.name, svc.host, m+"-v1", `["/`+m+`"]`, "main;"+m+"Func 10")
		exec(`INSERT INTO client_events (created_at, received_at, app, app_version, session_id, user_id, device, os, country, kind, name, url, method, status, duration_ms, message, stack, fingerprint)
VALUES (?, ?, ?, ?, ?, ?, 'desktop', 'linux', 'DE', 'error', ?, ?, 'GET', 500, 120, ?, ?, ?)`, ts, ts, svc.web, m+"-1", "sess"+svc.num, m+"-uid", m+"Name", "https://"+m+".example/x", m+" js error", m+".js:1", "cf"+svc.num)
		exec(`INSERT INTO client_events (created_at, received_at, app, session_id, kind, name, url, value) VALUES (?, ?, ?, ?, 'vital', 'LCP', ?, 2500)`, ts, ts, svc.web, "sess"+svc.num, "https://"+m+".example/x")
		exec(`INSERT INTO client_events (created_at, received_at, app, session_id, kind, name, url, method, status, duration_ms) VALUES (?, ?, ?, ?, 'request', 'fetch', ?, 'GET', 500, 300)`, ts, ts, svc.web, "sess"+svc.num, "https://"+m+".example/api")
		exec(`INSERT INTO ci_pipelines (created_at, updated_at, run_key, provider, repo, workflow, run_id, branch, commit_sha, commit_message, actor, status, conclusion, started_at, finished_at, duration_ms, service)
VALUES (?, ?, ?, 'github', ?, ?, ?, 'main', ?, ?, ?, 'completed', 'failure', ?, ?, 1000, ?)`, ts, ts, "run"+svc.num, "org/"+m, m+" build", svc.num, "sha"+svc.num, m+" commit", m+"-dev", ts, ts, svc.name)
		exec(`INSERT INTO ci_jobs (created_at, updated_at, job_key, run_key, provider, repo, workflow, name, branch, commit_sha, service, status, conclusion, started_at, finished_at, duration_ms)
VALUES (?, ?, ?, ?, 'github', ?, ?, ?, 'main', ?, ?, 'completed', 'failure', ?, ?, 900)`, ts, ts, "job"+svc.num, "run"+svc.num, "org/"+m, m+" build", m+" test", "sha"+svc.num, svc.name, ts, ts)
		exec(`INSERT INTO ci_tests (created_at, run_key, provider, repo, service, branch, commit_sha, job, suite, test, test_hash, status, duration_ms, message)
VALUES (?, ?, 'github', ?, ?, 'main', ?, ?, ?, ?, ?, 'failed', 10, ?)`, ts, "run"+svc.num, "org/"+m, svc.name, "sha"+svc.num, m+" test", m+"Suite", m+"Test", "hash"+svc.num, m+" assert")
		exec(`INSERT INTO ci_flaky_tests (created_at, test_hash, test, service, repo, reason) VALUES (?, ?, ?, ?, ?, ?)`, ts, "hash"+svc.num, m+"Test", svc.name, "org/"+m, m+" flaked")
		exec(`INSERT INTO host_inventory (host, service, os, kernel, arch, cpu_count, mem_total_mb, agent_version, ip, first_seen, last_seen) VALUES (?, ?, 'linux', ?, 'amd64', 2, 1024, '1', ?, ?, ?)`,
			svc.host, svc.name, m+"-kernel", "10.1.0."+svc.num, ts, ts)
		exec(`INSERT INTO host_processes (created_at, host, pid, name, username, cpu_percent, rss_mb, cmdline) VALUES (?, ?, 1, ?, 'root', 5, 10, ?)`, ts, svc.host, m+"d", "/usr/bin/"+m+"d")
		exec(`INSERT INTO host_mounts (created_at, host, mount, device, fstype, used_percent, free_gb, total_gb) VALUES (?, ?, ?, 'sda', 'ext4', 50, 10, 20)`, ts, svc.host, "/"+m)
		exec(`INSERT INTO host_units (created_at, host, unit, state, sub) VALUES (?, ?, ?, 'failed', 'failed')`, ts, svc.host, m+".service")
		exec(`INSERT INTO host_containers (created_at, host, container_id, name, image, project, state, health, restarts) VALUES (?, ?, ?, ?, ?, ?, 'running', 'healthy', 2)`,
			ts, svc.host, "cid"+svc.num, m+"-ctr", m+"/img:1", m+"-proj")
		exec(`INSERT INTO container_metrics (created_at, host, container, cpu_percent, mem_used_mb, mem_limit_mb, mem_percent) VALUES (?, ?, ?, 5, 10, 100, 10)`, ts, svc.host, m+"-ctr")
		exec(`INSERT INTO container_events (created_at, host, container, image, kind, severity, exit_code, detail) VALUES (?, ?, ?, ?, 'oom', 'critical', 137, ?)`, ts, svc.host, m+"-ctr", m+"/img:1", m+" killed")
		exec(`INSERT INTO security_events (created_at, source, kind, severity, host, service, actor, ip, target, detail, count) VALUES (?, 'api', 'brute_force', 'critical', ?, ?, ?, ?, ?, '{}', 9)`,
			ts, svc.host, svc.name, m+"-actor", "10.2.0."+svc.num, "/"+m+"/login")
		exec(`INSERT INTO security_known (account, kind, value, first_seen, last_seen) VALUES (?, 'ip', ?, ?, ?)`, "acct"+svc.num, "10.0.0."+svc.num, ts, ts)
		exec(`INSERT INTO explain_requests (created_at, target, statement, status, plan) VALUES (?, ?, ?, 'done', ?)`, ts, m+"-db", "SELECT "+m, m+" seq scan")
		exec(`INSERT INTO log_archives (hour, object, `+s.q.ident("lines")+`, bytes, created_at) VALUES (?, ?, 1, 1, ?)`, ts, m+"/obj", ts)
		exec(`INSERT INTO incidents (title, severity, status, services, impact, source_kind, source_ref, created_by, started_at, declared_at, updated_at)
VALUES (?, 'sev1', 'investigating', ?, ?, '', '', ?, ?, ?, ?)`, m+" outage", `["`+svc.name+`"]`, m+" impact", m+"-oncall", ts, ts, ts)
		exec(`INSERT INTO incident_timeline (incident_id, at, kind, actor, body, detail) VALUES (?, ?, 'note', ?, ?, '{}')`, svc.num, ts, m+"-oncall", m+" note")
		exec(`INSERT INTO incident_roles (incident_id, role, user_name, assigned_by, assigned_at) VALUES (?, 'commander', ?, 'x', ?)`, svc.num, m+"-cmdr", ts)
		exec(`INSERT INTO incident_actions (incident_id, body, owner, created_at) VALUES (?, ?, ?, ?)`, svc.num, m+" action", m+"-owner", ts)
		exec(`INSERT INTO alert_rules (name, kind, spec, severity, channels, runbook, created_at, updated_at, workspace_id) VALUES (?, 'threshold', ?, 'critical', '[]', '', ?, ?, ?)`,
			m+" rule", `{"metric":"error_rate","scope":{},"op":">","threshold":1,"window":"5m"}`, ts, ts, svc.num)
		exec(`INSERT INTO alert_events (created_at, rule_id, rule_name, kind, severity, state, series_key, value, threshold, message, explanation) VALUES (?, ?, ?, 'threshold', 'critical', 'firing', '', 9, 1, ?, '')`,
			ts, svc.num, m+" rule", m+" fired")
		exec(`INSERT INTO alert_state (rule_id, series_key, label, state, state_since, value, threshold, message, recent) VALUES (?, '', ?, 'firing', ?, 9, 1, ?, '')`, svc.num, m+" label", ts, m+" firing")
		exec(`INSERT INTO alert_incidents (rule_id, series_key, opened_at, ack_note) VALUES (?, '', ?, ?)`, svc.num, ts, m+" ack")
		exec(`INSERT INTO alert_rule_history (rule_id, at, actor, action, changes, rule) VALUES (?, ?, ?, 'create', '[]', ?)`, svc.num, ts, m+"-editor", `{"name":"`+m+` rule"}`)
		exec(`INSERT INTO uptime_checks (name, url, headers, body_contains, created_at, updated_at, workspace_id) VALUES (?, ?, '{}', '', ?, ?, ?)`, m+" check", "https://"+m+".example/health", ts, ts, svc.num)
		exec(`INSERT INTO uptime_results (created_at, check_id, check_name, url, ok, status, duration_ms, error) VALUES (?, ?, ?, ?, 0, 500, 100, ?)`,
			ts, svc.num, m+" check", "https://"+m+".example/health", m+" down")
		exec(`INSERT INTO dashboards (name, description, layout, created_by, created_at, updated_at, workspace_id) VALUES (?, ?, '{"panels":[],"variables":[]}', ?, ?, ?, ?)`,
			m+" dash", m+" board", m+"-author", ts, ts, svc.num)
		exec(`INSERT INTO log_views (name, query, created_by, created_at, workspace_id) VALUES (?, ?, ?, ?, ?)`, m+" view", "service:"+svc.name, m+"-author", ts, svc.num)
		exec(`INSERT INTO replay_sessions (session_id, started_at, created_at, app, user_id, url, reason, chunks, bytes) VALUES (?, ?, ?, ?, ?, ?, 'error', 1, 10)`,
			svc.num, ts, ts, svc.web, m+"-uid", "https://"+m+".example/x")
		exec(`INSERT INTO replay_chunks (session_id, seq, created_at, ended_at, events, bytes, payload) VALUES (?, 0, ?, ?, 1, 10, ?)`, svc.num, ts, ts, gzipB64(`[{"text":"`+m+` typed"}]`))
		exec(`INSERT INTO uptime_browser_checks (check_id, steps, secrets) VALUES (?, ?, '{}')`, svc.num, `[{"action":"goto","url":"https://`+m+`.example/login","label":"`+m+` step"}]`)
		exec(`INSERT INTO uptime_browser_results (created_at, check_id, ok, failed_step, steps, console, network) VALUES (?, ?, 0, 0, ?, ?, '[]')`,
			ts, svc.num, `[{"label":"`+m+` step","ok":false,"error":"`+m+` timeout"}]`, `["`+m+` console"]`)
		exec(`INSERT INTO uptime_check_tags (check_id, tag) VALUES (?, ?)`, svc.num, m+"-tag")
		exec(`INSERT INTO uptime_playwright_checks (check_id, test_key, project, title) VALUES (?, ?, ?, ?)`, svc.num, "key"+svc.num, m+"-project", m+".spec.ts › works")
		exec(`INSERT INTO uptime_playwright_results (created_at, check_id, ok, status, steps, attachments, error, project) VALUES (?, ?, 0, 'failed', ?, '[]', ?, ?)`,
			ts, svc.num, `[{"title":"`+m+` step","duration_ms":1}]`, m+" assertion", m+"-project")
		exec(`INSERT INTO uptime_run_batches (id, created_at, deadline_at, status, workspace_id, release_name, commit_sha, tags, created_by, total)
VALUES (?, ?, ?, 'failed', ?, ?, ?, '[]', ?, 1)`, svc.num, ts, ts, svc.num, m+"-release", "abcdef"+svc.num, m+"-ci")
		exec(`INSERT INTO uptime_run_items (batch_id, created_at, check_id, check_name, kind, status, error) VALUES (?, ?, ?, ?, 'http', 'failed', ?)`,
			svc.num, ts, svc.num, m+" check", m+" failed")
		exec(`INSERT INTO security_events (created_at, source, kind, severity, host, service, actor, ip, target, detail, count) VALUES (?, 'rule', ?, 'high', ?, ?, ?, ?, ?, ?, 3)`,
			ts, "rule_"+svc.num, svc.host, svc.name, m+"-attacker", "10.3.0."+svc.num, "/"+m+"/admin", `{"rule":"`+m+` rule"}`)
		exec(`INSERT INTO security_rules (name, kind, enabled, spec, created_at, created_by, updated_at) VALUES (?, ?, 1, ?, ?, 'x', ?)`,
			m+" detection", "rule_"+svc.num, `{"logic":"threshold","source":"api","threshold":5,"window":"5m","where":"path LIKE '/`+m+`%'"}`, ts, ts)
	}
	exec(`INSERT INTO dashboards (name, description, layout, created_by, created_at, updated_at) VALUES ('Shared board', '', '{"panels":[],"variables":[]}', '', ?, ?)`, ts, ts)
	// Rows without a service are seen only by people who see everything.
	exec(`INSERT INTO api_logs (request_id, created_at, method, path, route, status, duration_ms) VALUES ('reqnull', ?, 'GET', ?, ?, 503, 10)`, ts, "/"+leak+"-null", "/"+leak+"-null")

	f.scoped = f.key(t, "scoped")
	f.unscoped = f.key(t, "everyone")
	scopedID := strings.TrimPrefix(f.keyID(t, "scoped"), "key:")
	exec(`INSERT INTO workspaces (id, name, services, members, created_by, created_at, updated_at) VALUES (1, 'Hidden', ?, '[]', 't', ?, ?)`,
		`["`+leak+`-svc","`+leak+`-web"]`, ts, ts)
	exec(`INSERT INTO workspaces (id, name, services, members, created_by, created_at, updated_at) VALUES (2, 'Payments', '["pay-*"]', ?, 't', ?, ?)`,
		`[{"kind":"key","id":"`+scopedID+`","role":"viewer"}]`, ts, ts)
	s.dropWorkspaces()
	// Rollups over the raw rows, for the long ranges.
	rt := &rollupRuntime{s: s, spans: map[string]rollupSpan{}}
	rollupRuntimes.Store(s, rt)
	t.Cleanup(func() { rollupRuntimes.Delete(s) })
	for _, k := range rt.kinds() {
		_ = rt.forward(ctx, k, time.Now().UTC().Add(5*time.Minute))
	}
	return f
}

func (f *wsFixture) key(t *testing.T, name string) string {
	t.Helper()
	raw, err := newKeySecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.Exec(context.Background(), `INSERT INTO visionary_api_keys (name, scope, prefix, key_hash, created_by, created_at) VALUES (?, 'read', ?, ?, 't', ?)`,
		name, raw[:keyVisibleLen], hashKey(raw), nowTS()); err != nil {
		t.Fatal(err)
	}
	f.s.invalidateKeys()
	return raw
}

func (f *wsFixture) keyID(t *testing.T, name string) string {
	res, err := f.s.db.Query(context.Background(), `SELECT id FROM visionary_api_keys WHERE name = ?`, name)
	if err != nil || len(res.Rows) == 0 {
		t.Fatal(err)
	}
	return "key:" + toString(res.Rows[0][0])
}

func (f *wsFixture) get(key, target string, timeout time.Duration) *httptest.ResponseRecorder {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r := httptest.NewRequest("GET", target, nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	// Replay chunks come gzipped; read what they hold.
	if w.Header().Get("Content-Encoding") == "gzip" {
		if zr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes())); err == nil {
			plain, _ := io.ReadAll(zr)
			w.Body = bytes.NewBuffer(plain)
		}
	}
	return w
}

func gzipB64(s string) string {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func (f *wsFixture) post(key, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", target, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

type route struct {
	method, path string
	authed       bool
}

var routeLine = regexp.MustCompile(`mux\.Handle(?:Func)?\(\s*(p\+)?"(?:([A-Z]+) )?(/[^"]*)"(.*)`)

// sourceRoutes reads every route the package registers from its source, so a
// new route can't escape the test without being listed.
func sourceRoutes(t *testing.T) []route {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []route
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range routeLine.FindAllStringSubmatch(string(b), -1) {
			authed := strings.Contains(m[4], "s.authed(") || strings.Contains(m[4], "s.promAuthed(")
			if m[1] != "" {
				for _, p := range promAPIPrefixes {
					out = append(out, route{"GET", p + m[3], authed})
				}
				continue
			}
			method := m[2]
			if method == "" {
				method = "GET"
			}
			out = append(out, route{method, m[3], authed})
		}
	}
	return out
}

// readPOSTs are the POST routes that only read, with a body to send.
var readPOSTs = map[string]string{
	"/api/query":                    `{"query":"SELECT service, path, route FROM api_logs UNION ALL SELECT app, url, message FROM client_events","from":"%FROM%","to":"%TO%"}`,
	"/api/settings/logs/rules/test": `{"rules":[],"sample":"x"}`,
	"/api/applogs/ask":              `{"question":"errors"}`,
	"/api/applogs/explain":          `{"id":1}`,
	"/api/analyst":                  `{"question":"errors"}`,
}

// leakExcluded are the authed GET routes not called, and why.
var leakExcluded = map[string]string{
	"/api/sso/{id}/test": "redirects to an identity provider; reads no data",
}

// pathValues fill route parameters with the hidden service's objects, so
// every direct link is tried too. None holds the marker.
var pathValues = map[string]string{
	"{id}": "1", "{fingerprint}": "f00d0001", "{request_id}": "req1", "{trace_id}": secretTrace,
	"{host}": leak + "-host", "{account}": "acct1", "{ip}": "10.0.0.1", "{name}": "service", "{seq}": "0", "{run}": "1",
}

// panelRoutes expand {panel} into every panel the route knows.
func (f *wsFixture) panels(path string) []string {
	s := f.s
	keys := func(m any) []string {
		var out []string
		switch t := m.(type) {
		case map[string]string:
			for k := range t {
				out = append(out, k)
			}
		case map[string]containerPanel:
			for k := range t {
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return out
	}
	switch path {
	case "/api/overview/{panel}":
		return keys(s.overview)
	case "/api/system/{panel}":
		return keys(s.system)
	case "/api/performance/{panel}":
		return keys(s.performance())
	case "/api/rum/{panel}":
		return append(keys(s.rumPanels()), "api")
	case "/api/security/panels/{panel}":
		return keys(s.security().panels)
	case "/api/containers/panels/{panel}":
		return keys(containerPanels(s.q))
	case "/api/uptime/checks/{id}/{panel}":
		return keys(uptimePanels)
	}
	return nil
}

var param = regexp.MustCompile(`\{[a-z_]+\}`)

// TestWorkspaceLeaks calls every data route as a read key scoped to the
// Payments workspace and as one that sees everything, and requires that no
// response for the scoped key holds a row of the hidden service.
func TestWorkspaceLeaks(t *testing.T) {
	f := newWSFixture(t)
	from := f.at.Add(-time.Hour).Format(time.RFC3339)
	to := f.at.Add(time.Hour).Format(time.RFC3339)
	ranges := []string{"from=" + from + "&to=" + to, "from=" + f.at.Add(-7*24*time.Hour).Format(time.RFC3339) + "&to=" + to}
	extra := "&range=1h&start=" + from + "&end=" + to + "&step=60&query=orders_total&match[]=orders_total&kind=db&by=app&window=1h"

	type call struct{ name, target string }
	var calls []call
	for _, rt := range sourceRoutes(t) {
		if !rt.authed || rt.method != "GET" {
			continue
		}
		if why, ok := leakExcluded[rt.path]; ok {
			t.Logf("not called: %s (%s)", rt.path, why)
			continue
		}
		paths := []string{rt.path}
		if strings.Contains(rt.path, "{panel}") {
			paths = nil
			for _, p := range f.panels(rt.path) {
				paths = append(paths, strings.Replace(rt.path, "{panel}", p, 1))
			}
			if len(paths) == 0 {
				t.Fatalf("no panels known for %s", rt.path)
			}
		}
		for _, p := range paths {
			p = param.ReplaceAllStringFunc(p, func(k string) string {
				if v, ok := pathValues[k]; ok {
					return v
				}
				t.Fatalf("no value for %s in %s", k, rt.path)
				return ""
			})
			for i, rg := range ranges {
				if i > 0 && !strings.Contains(p, "/overview/") && !strings.Contains(p, "/performance/") && !strings.Contains(p, "/system/") {
					continue
				}
				calls = append(calls, call{rt.method + " " + rt.path, p + "?" + rg + extra})
			}
		}
	}

	seen, ok, broken := 0, 0, []string{}
	for _, c := range calls {
		timeout := 20 * time.Second
		if strings.HasSuffix(c.target[:strings.Index(c.target, "?")], "/tail") {
			timeout = 2500 * time.Millisecond
		}
		all := f.get(f.unscoped, c.target, timeout)
		mine := f.get(f.scoped, c.target, timeout)
		if mine.Code < 300 {
			ok++
		}
		if strings.Contains(strings.ToLower(all.Body.String()), leak) {
			seen++
		}
		if body := strings.ToLower(mine.Body.String()); strings.Contains(body, leak) {
			t.Errorf("LEAK %s → %d: %.400s", c.target, mine.Code, mine.Body.String())
		}
		if mine.Code >= 500 || mine.Code == 400 && all.Code < 300 {
			broken = append(broken, c.target+" → "+strconv.Itoa(mine.Code)+" "+strings.TrimSpace(mine.Body.String()))
		}
	}
	for path, body := range readPOSTs {
		body = strings.NewReplacer("%FROM%", from, "%TO%", to).Replace(body)
		all := f.post(f.unscoped, path, body)
		mine := f.post(f.scoped, path, body)
		if strings.Contains(strings.ToLower(all.Body.String()), leak) {
			seen++
		}
		if strings.Contains(strings.ToLower(mine.Body.String()), leak) {
			t.Errorf("LEAK POST %s → %d: %.400s", path, mine.Code, mine.Body.String())
		}
		calls = append(calls, call{"POST " + path, path})
	}
	for _, b := range broken {
		t.Errorf("scope broke %s", b)
	}
	// The hidden rows must be there for someone, or the test proves nothing.
	if seen < 80 {
		t.Errorf("only %d of %d responses showed the hidden service to the unscoped key", seen, len(calls))
	}
	t.Logf("%d calls, %d answered for the scoped key; the unscoped key saw the hidden service in %d", len(calls), ok, seen)

	// The newer features' hidden rows are really there for the unscoped key.
	for _, p := range []string{"/api/rum/replay/1", "/api/rum/replay/1/chunk/0", "/api/uptime/checks/1/runs", "/api/uptime/checks/1/runs/1", "/api/security/rules", "/api/security/findings"} {
		w := f.get(f.unscoped, p+"?"+ranges[0], 10*time.Second)
		if !strings.Contains(w.Body.String(), leak) {
			t.Errorf("unscoped %s shows no hidden row (%d): %.200s", p, w.Code, w.Body)
		}
	}
	// The scoped key does see its own service.
	w := f.get(f.scoped, "/api/applogs?"+ranges[0], 10*time.Second)
	if !strings.Contains(w.Body.String(), "payok line failed") {
		t.Errorf("scoped key can't see its own logs: %s", w.Body)
	}
	w = f.get(f.scoped, "/api/session", 5*time.Second)
	if !strings.Contains(w.Body.String(), `"scoped":true`) || !strings.Contains(w.Body.String(), "Payments") {
		t.Errorf("session: %s", w.Body)
	}
}

// Requests made inside a workspace can't change what belongs to another.
func TestWorkspaceWrites(t *testing.T) {
	f := newWSFixture(t)
	s := f.s
	ctx := context.Background()
	editor := &session{UserID: "acct:ed", Name: "Ed", Role: "editor"}
	// Ed is an editor in Payments only.
	if err := s.db.Exec(ctx, `UPDATE workspaces SET members = '[{"kind":"user","id":"ed","role":"editor"}]' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	s.dropWorkspaces()
	do := func(method, target, body string, h http.HandlerFunc, values map[string]string) int {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		for k, v := range values {
			r.SetPathValue(k, v)
		}
		w := httptest.NewRecorder()
		s.serveAs(w, r, editor, h)
		return w.Code
	}
	do("DELETE", "/api/dashboards/1", "", s.handleDeleteDashboard, map[string]string{"id": "1"})
	do("DELETE", "/api/alerts/rules/1", "", s.handleDeleteRule, map[string]string{"id": "1"})
	do("DELETE", "/api/applogs/views/1", "", s.handleDeleteLogView, map[string]string{"id": "1"})
	for _, table := range []string{"dashboards", "alert_rules", "log_views"} {
		res, err := s.db.Query(ctx, `SELECT COUNT(*) FROM `+table+` WHERE id = 1`)
		if err != nil || toInt(res.Rows[0][0]) != 1 {
			t.Errorf("%s 1 was deleted from outside its workspace", table)
		}
	}
	// What Ed creates belongs to Payments.
	if code := do("POST", "/api/dashboards", `{"name":"Ed's board","layout":{"panels":[],"variables":[]}}`, s.handleCreateDashboard, nil); code != http.StatusCreated {
		t.Fatalf("create dashboard: %d", code)
	}
	res, _ := s.db.Query(ctx, `SELECT workspace_id FROM dashboards WHERE name = 'Ed''s board'`)
	if len(res.Rows) != 1 || toInt(res.Rows[0][0]) != 2 {
		t.Errorf("new dashboard's workspace: %v", res.Rows)
	}
	// Detection rules cover every service, so they're changed from outside workspaces.
	if code := do("DELETE", "/api/security/rules/1", "", s.handleDeleteDetectionRule, map[string]string{"id": "1"}); code != http.StatusForbidden {
		t.Errorf("workspace editor deleted a detection rule: %d", code)
	}
	// Configuration export and import span every workspace: refused inside
	// one, even for an admin who narrowed the pages to it.
	admin := &session{UserID: "acct:root", Name: "Root", Role: "admin"}
	for _, c := range []struct {
		method, target string
		h              http.HandlerFunc
	}{{"GET", "/api/config/export", s.handleConfigExport}, {"POST", "/api/config/import", s.handleConfigImport}} {
		r := httptest.NewRequest(c.method, c.target, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: workspaceCookie, Value: "2"})
		w := httptest.NewRecorder()
		s.serveAs(w, r, admin, c.h)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s inside a workspace: %d", c.target, w.Code)
		}
		if code := do(c.method, c.target, `{}`, c.h, nil); code != http.StatusForbidden {
			t.Errorf("%s by a workspace editor: %d", c.target, code)
		}
	}
	// Editors in a workspace aren't admins of the server.
	if code := do("GET", "/api/team/keys", "", s.handleListKeys, nil); code != http.StatusForbidden {
		t.Errorf("workspace editor listed API keys: %d", code)
	}
}

// With no workspaces nothing changes: no request is scoped and statements
// run as written.
func TestNoWorkspacesNoScope(t *testing.T) {
	f := newWSFixture(t)
	s := f.s
	if err := s.db.Exec(context.Background(), `DELETE FROM workspaces`); err != nil {
		t.Fatal(err)
	}
	s.dropWorkspaces()
	for _, key := range []string{f.scoped, f.unscoped} {
		w := f.get(key, "/api/applogs?from="+f.at.Add(-time.Hour).Format(time.RFC3339)+"&to="+f.at.Add(time.Hour).Format(time.RFC3339), 10*time.Second)
		if !strings.Contains(w.Body.String(), leak+" line failed") || !strings.Contains(w.Body.String(), "payok line failed") {
			t.Errorf("without workspaces a key should see every service: %.300s", w.Body)
		}
	}
	r := httptest.NewRequest("GET", "/api/session", nil)
	sc, err := s.scopeFor(r, &session{UserID: "key:1", Role: "viewer"})
	if err != nil || sc != nil {
		t.Errorf("scope without workspaces: %v %v", sc, err)
	}
}

// Every table is either filtered in a workspace or known to hold no
// service's data.
func TestEveryTableClassified(t *testing.T) {
	f := newWSFixture(t)
	shared := map[string]bool{
		"ANALYTICS_PANELS": true, "VISIONARY_SETTINGS": true, "VISIONARY_USERS": true, "VISIONARY_API_KEYS": true,
		"VISIONARY_SCIM_GROUPS": true, "AUDIT_LOG": true, "ALERT_CHANNELS": true, "ALERT_SILENCES": true,
		"ROLLUP_STATE": true, "SECURITY_SUPPRESSIONS": true, "WORKSPACES": true, "SQLITE_SEQUENCE": true,
		"VISIONARY_LEASES": true, "VISIONARY_REPLICAS": true, "VISIONARY_CHANGES": true, "VISIONARY_ONCE": true,
	}
	for _, table := range testTables(t, f.s) {
		name := strings.ToUpper(table)
		if _, ok := scopedTables[name]; !ok && !shared[name] {
			t.Errorf("table %s is neither filtered by workspaces nor listed as shared", name)
		}
	}
}
