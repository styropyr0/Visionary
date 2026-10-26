package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// securityTestServer is a server on a fresh SQLite file with config applied
// before the detectors read their settings.
func securityTestServer(t *testing.T, config map[string]string) *Server {
	t.Helper()
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "security.db"))
	s, err := New(Config{DatabaseURL: "sqlite:///" + path}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.db.Close() })
	l := defaultLiveSettings()
	for k, v := range config {
		l.Config[k] = v
	}
	s.liveSet.p.Store(l)
	securityStates.Delete(s)
	return s
}

// req is one api_logs row; zero fields are NULL or defaults.
type req struct {
	at                         time.Time
	method, path, route, query string
	status                     int
	ip, ua, handle, login      string
}

func addReqs(t *testing.T, s *Server, rows ...req) {
	t.Helper()
	for i, r := range rows {
		if err := s.db.Exec(context.Background(), `INSERT INTO api_logs (request_id, created_at, method, path, route, query, status, duration_ms, ip, user_agent, user_handle, login_account, service)
VALUES (?, ?, ?, ?, ?, ?, ?, 5, ?, ?, ?, ?, 'shop')`,
			fmt.Sprintf("r%d", i), r.at.UTC().Format(tsLayout), fallback(r.method, "GET"), r.path, nullable(r.route), nullable(r.query), r.status,
			nullable(r.ip), nullable(r.ua), nullable(r.handle), nullable(r.login)); err != nil {
			t.Fatal(err)
		}
	}
}

type storedFinding struct {
	source, kind, severity, ip, actor, target, host string
	count                                           int64
	detail                                          map[string]any
}

func findingsOf(t *testing.T, s *Server) map[string][]storedFinding {
	t.Helper()
	res, err := s.db.Query(context.Background(), `SELECT source, kind, severity, COALESCE(ip, ''), COALESCE(actor, ''), COALESCE(target, ''), COALESCE(host, ''), count, detail
FROM security_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]storedFinding{}
	for _, row := range res.Rows {
		f := storedFinding{toString(row[0]), toString(row[1]), toString(row[2]), toString(row[3]), toString(row[4]), toString(row[5]), toString(row[6]), toInt(row[7]), nil}
		_ = json.Unmarshal([]byte(toString(row[8])), &f.detail)
		out[f.kind] = append(out[f.kind], f)
	}
	return out
}

func one(t *testing.T, got map[string][]storedFinding, kind string) storedFinding {
	t.Helper()
	if len(got[kind]) != 1 {
		t.Fatalf("%s: want 1 finding, got %d (all: %v)", kind, len(got[kind]), kinds(got))
	}
	return got[kind][0]
}

func kinds(got map[string][]storedFinding) []string {
	var out []string
	for k, v := range got {
		out = append(out, fmt.Sprintf("%s×%d", k, len(v)))
	}
	return out
}

func runOK(t *testing.T, s *Server, now time.Time) {
	t.Helper()
	s.runDetectors(context.Background(), now)
	if err := s.security().lastErr; err != "" {
		t.Fatal(err)
	}
}

const (
	chrome       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"
	firefoxLinux = "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0"
)

func TestLoginDetectors(t *testing.T) {
	s := securityTestServer(t, nil)
	now := time.Now().UTC()
	at := now.Add(-2 * time.Minute)
	var rows []req
	// One IP failing on six accounts: stuffing, counted by login_account.
	for i := 0; i < 6; i++ {
		rows = append(rows, req{at: at, method: "POST", path: "/api/login", status: 401, ip: "203.0.113.5", login: fmt.Sprintf("user%d@x.io", i)})
	}
	// Five IPs failing on alice, then one of them gets in.
	for i := 0; i < 5; i++ {
		rows = append(rows, req{at: at, method: "POST", path: "/api/login", status: 401, ip: fmt.Sprintf("198.51.100.%d", i+1), login: "alice@x.io"})
	}
	rows = append(rows, req{at: at.Add(time.Second), method: "POST", path: "/api/login", status: 200, ip: "198.51.100.3", login: "alice@x.io"})
	// Bob's browser tries carol's account three times.
	rows = append(rows, req{at: at.Add(-time.Minute), path: "/api/orders", route: "/api/orders", status: 200, ip: "192.0.2.10", ua: chrome, handle: "bob@x.io"})
	for i := 0; i < 3; i++ {
		rows = append(rows, req{at: at, method: "POST", path: "/api/login", status: 401, ip: "192.0.2.10", ua: chrome, login: "carol@x.io"})
	}
	addReqs(t, s, rows...)
	runOK(t, s, now)
	got := findingsOf(t, s)
	if f := one(t, got, "credential_stuffing"); f.ip != "203.0.113.5" || toInt(f.detail["accounts"]) != 6 {
		t.Fatalf("stuffing: %+v", f)
	}
	f := one(t, got, "account_targeted")
	if f.actor != "alice@x.io" || f.severity != "critical" || f.detail["succeeded_from"] == nil {
		t.Fatalf("targeted: %+v", f)
	}
	f = one(t, got, "login_as_other")
	if f.actor != "bob@x.io" || f.target != "carol@x.io" || f.severity != "warning" {
		t.Fatalf("login as other: %+v", f)
	}
	// A second run in the same period adds nothing new.
	runOK(t, s, now.Add(time.Second))
	if again := findingsOf(t, s); len(again["credential_stuffing"]) != 1 || again["credential_stuffing"][0].count != 6 {
		t.Fatalf("dedupe: %+v", again["credential_stuffing"])
	}
}

func TestPortalFindings(t *testing.T) {
	s := securityTestServer(t, nil)
	now := time.Now().UTC()
	ctx := context.Background()
	at := now.Add(-time.Minute).Format(tsLayout)
	for i := 0; i < 6; i++ {
		if err := s.db.Exec(ctx, `INSERT INTO audit_log (created_at, user_id, user_name, role, action, target, detail, ip) VALUES (?, '', '', '', 'auth.sign_in_failed', 'ad***', '{}', '203.0.113.9')`, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Exec(ctx, `INSERT INTO audit_log (created_at, user_id, user_name, role, action, target, detail, ip) VALUES (?, 'u1', 'Ana', 'admin', 'settings.secret_viewed', 'smtp_password', '{}', '192.0.2.1')`, at); err != nil {
		t.Fatal(err)
	}
	runOK(t, s, now)
	got := findingsOf(t, s)
	if f := one(t, got, "portal_bruteforce"); f.source != "audit" || f.ip != "203.0.113.9" || f.count != 6 {
		t.Fatalf("portal brute force: %+v", f)
	}
	if f := one(t, got, "portal_secret_viewed"); f.actor != "Ana" || !strings.Contains(f.detail["summary"].(string), "smtp_password") {
		t.Fatalf("secret viewed: %+v", f)
	}
	// The same audit entry is not reported twice.
	runOK(t, s, now.Add(time.Minute))
	if n := len(findingsOf(t, s)["portal_secret_viewed"]); n != 1 {
		t.Fatalf("secret viewed again: %d", n)
	}
}

func TestSlowAndDistributed(t *testing.T) {
	s := securityTestServer(t, map[string]string{"security_distributed_ips": "20"})
	now := time.Now().UTC()
	var rows []req
	// 35 failures from one IP, two an hour: never 10 in one 5-minute window.
	for i := 0; i < 35; i++ {
		rows = append(rows, req{at: now.Add(-time.Duration(i*30+1) * time.Minute), method: "POST", path: "/api/login", status: 401, ip: "203.0.113.77", login: "dan@x.io"})
	}
	// 25 IPs failing twice each.
	for i := 0; i < 25; i++ {
		for j := 0; j < 2; j++ {
			rows = append(rows, req{at: now.Add(-time.Duration(i+j*60+1) * time.Minute), method: "POST", path: "/api/login", status: 401, ip: fmt.Sprintf("100.64.1.%d", i), login: "erin@x.io"})
		}
	}
	addReqs(t, s, rows...)
	runOK(t, s, now)
	got := findingsOf(t, s)
	if f := one(t, got, "auth_bruteforce_slow"); f.ip != "203.0.113.77" || f.count < 30 {
		t.Fatalf("slow: %+v", f)
	}
	if f := one(t, got, "distributed_login_attack"); toInt(f.detail["ips"]) != 25 {
		t.Fatalf("distributed: %+v", f)
	}
	if f := one(t, got, "account_targeted_slow"); f.actor != "erin@x.io" {
		t.Fatalf("targeted slow: %+v", f)
	}
	if len(got["auth_bruteforce"]) != 0 {
		t.Fatal("fast brute force should not fire")
	}
}

func TestTakeoverDetectors(t *testing.T) {
	s := securityTestServer(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	// An earlier sign-in builds frank's history.
	addReqs(t, s, req{at: now.Add(-20 * time.Minute), method: "POST", path: "/api/login", status: 200, ip: "192.0.2.50", ua: chrome, login: "frank@x.io"})
	runOK(t, s, now.Add(-15*time.Minute))
	// Then a new browser and system, followed by an email change.
	addReqs(t, s,
		req{at: now.Add(-2 * time.Minute), method: "POST", path: "/api/login", status: 200, ip: "192.0.2.51", ua: firefoxLinux, login: "frank@x.io"},
		req{at: now.Add(-90 * time.Second), method: "PUT", path: "/api/me/email", route: "/api/me/email", status: 200, ip: "192.0.2.51", ua: firefoxLinux, handle: "frank@x.io"},
	)
	// A risky finding from earlier makes the change stand out.
	if err := s.db.Exec(ctx, `INSERT INTO security_events (created_at, source, kind, severity, actor, ip, detail, count) VALUES (?, 'api', 'new_login_location', 'warning', 'frank@x.io', '192.0.2.51', '{}', 1)`,
		now.Add(-3*time.Minute).Format(tsLayout)); err != nil {
		t.Fatal(err)
	}
	runOK(t, s, now)
	got := findingsOf(t, s)
	if f := one(t, got, "new_login_device"); f.actor != "frank@x.io" || f.target != "Firefox on Linux" {
		t.Fatalf("new device: %+v", f)
	}
	if f := one(t, got, "risky_account_change"); f.actor != "frank@x.io" || f.severity != "critical" || !strings.Contains(f.target, "/api/me/email") {
		t.Fatalf("risky change: %+v", f)
	}
}

func TestSessionSpread(t *testing.T) {
	s := securityTestServer(t, map[string]string{"security_user_ips": "3"})
	now := time.Now().UTC()
	uas := []string{chrome, firefoxLinux, "okhttp/4.12"}
	var rows []req
	for i := 0; i < 4; i++ {
		rows = append(rows, req{at: now.Add(-2 * time.Minute), path: "/api/cart", route: "/api/cart", status: 200, ip: fmt.Sprintf("198.18.0.%d", i+1), ua: uas[i%3], handle: "gina@x.io"})
	}
	addReqs(t, s, rows...)
	runOK(t, s, now)
	if f := one(t, findingsOf(t, s), "session_spread"); f.actor != "gina@x.io" || toInt(f.detail["ips"]) != 4 {
		t.Fatalf("spread: %+v", f)
	}
}

func TestAbuseDetectors(t *testing.T) {
	s := securityTestServer(t, map[string]string{"security_route_limits": "POST /api/export* 2\nnot a rule"})
	now := time.Now().UTC()
	at := now.Add(-2 * time.Minute)
	var rows []req
	for i := 0; i < 60; i++ {
		rows = append(rows, req{at: at, path: fmt.Sprintf("/api/orders/%d", 1000+i), route: "/api/orders/:id", status: 200, ip: "192.0.2.60", handle: "hank@x.io"})
	}
	for i := 0; i < 25; i++ {
		rows = append(rows, req{at: at, path: fmt.Sprintf("/api/orders/%d", 2000+i), route: "/api/orders/:id", status: 403, ip: "192.0.2.61", handle: "ivy@x.io"})
	}
	for _, q := range []string{"id=1%27%20or%20%271%27=%271", "q=<script>alert(1)</script>", "x=${jndi:ldap://e.vil/a}"} {
		rows = append(rows, req{at: at, path: "/api/search", route: "/api/search", query: q, status: 500, ip: "203.0.113.66"})
	}
	for i := 0; i < 15; i++ {
		rows = append(rows, req{at: at, method: "POST", path: "/api/export/csv", route: "/api/export/:fmt", status: 200, ip: "192.0.2.62", handle: "jo@x.io"})
	}
	for i := 0; i < 12; i++ {
		rows = append(rows,
			req{at: at, method: "POST", path: "/api/signup", status: 201, ip: "203.0.113.70"},
			req{at: at, method: "POST", path: "/api/otp/send", status: 200, ip: "203.0.113.71", login: "kim@x.io"})
	}
	addReqs(t, s, rows...)
	runOK(t, s, now)
	got := findingsOf(t, s)
	if f := one(t, got, "enumeration"); f.actor != "hank@x.io" || toInt(f.detail["read_ids"]) != 60 {
		t.Fatalf("enumeration: %+v", f)
	}
	if f := one(t, got, "idor_probe"); f.actor != "ivy@x.io" || toInt(f.detail["denied_ids"]) != 25 {
		t.Fatalf("idor: %+v", f)
	}
	f := one(t, got, "injection_attempt")
	cats, _ := f.detail["categories"].(map[string]any)
	if f.ip != "203.0.113.66" || f.severity != "critical" || cats["sql"] == nil || cats["script"] == nil || cats["jndi"] == nil {
		t.Fatalf("injection: %+v", f)
	}
	if f := one(t, got, "route_rate_limit"); f.actor != "jo@x.io" || f.target != "POST /api/export*" {
		t.Fatalf("route limit: %+v", f)
	}
	if f := one(t, got, "signup_abuse"); f.ip != "203.0.113.70" {
		t.Fatalf("signup: %+v", f)
	}
	if n := len(got["otp_abuse"]); n != 2 { // per IP and per account
		t.Fatalf("otp: %d (%v)", n, kinds(got))
	}
}

func TestSuppressionsAndSSH(t *testing.T) {
	s := securityTestServer(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	at := now.Add(-2 * time.Minute)
	var rows []req
	for i := 0; i < 12; i++ {
		rows = append(rows,
			req{at: at, method: "POST", path: "/api/login", status: 401, ip: "203.0.113.20"},
			req{at: at, method: "POST", path: "/api/login", status: 401, ip: "203.0.113.21"})
	}
	addReqs(t, s, rows...)
	if err := s.db.Exec(ctx, `INSERT INTO security_suppressions (created_at, created_by, kind, ip) VALUES (?, 'test', '*', '203.0.113.20/32')`, nowTS()); err != nil {
		t.Fatal(err)
	}
	for kind, n := range map[string]int{"ssh_failed": 12, "ssh_login": 1} {
		if err := s.db.Exec(ctx, `INSERT INTO security_events (created_at, source, kind, severity, host, actor, ip, count) VALUES (?, 'host', ?, 'info', 'web-1', 'root', '203.0.113.30', ?)`,
			at.Format(tsLayout), kind, n); err != nil {
			t.Fatal(err)
		}
	}
	runOK(t, s, now)
	got := findingsOf(t, s)
	if f := one(t, got, "auth_bruteforce"); f.ip != "203.0.113.21" {
		t.Fatalf("suppressed IP still reported: %+v", got["auth_bruteforce"])
	}
	if f := one(t, got, "ssh_bruteforce"); f.host != "web-1" || f.severity != "critical" || f.count != 12 {
		t.Fatalf("ssh: %+v", f)
	}
}

func TestTOTP(t *testing.T) {
	// RFC 6238 appendix B, SHA-1, cut to six digits.
	secret := []byte("12345678901234567890")
	for _, c := range []struct {
		at   int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1234567890, "005924"}, {2000000000, "279037"}} {
		if got := totpAt(secret, c.at/totpStep); got != c.want {
			t.Fatalf("at %d: %s, want %s", c.at, got, c.want)
		}
	}
	now := time.Unix(1111111109, 0)
	if totpCheck(secret, "081804", now) < 0 || totpCheck(secret, "081 804", now) < 0 || totpCheck(secret, "000000", now) >= 0 {
		t.Fatal("check")
	}
}

func TestAuditExportVerify(t *testing.T) {
	key := []byte("k")
	prev := strings.Repeat("0", 64)
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for i := 1; i <= 3; i++ {
		l := auditLine{Seq: i, ID: int64(i), Action: "auth.sign_in", Detail: json.RawMessage(`{}`), Prev: prev}
		l.Hash = hashAuditLine(l)
		prev = l.Hash
		_ = enc.Encode(l)
	}
	sig := auditSignature{Type: "signature", Entries: 3, Head: prev}
	sig.HMAC = sig.mac(key)
	_ = enc.Encode(sig)
	good := b.String()
	if v := verifyAuditExport(good, key); v["valid"] != true {
		t.Fatalf("valid export: %v", v)
	}
	lines := strings.SplitAfter(good, "\n")
	for name, c := range map[string]struct {
		file string
		key  []byte
	}{
		"changed":   {strings.Replace(good, `"action":"auth.sign_in"`, `"action":"auth.sign_out"`, 1), key},
		"dropped":   {strings.Join(lines[1:], ""), key},
		"other key": {good, []byte("x")},
		"no sig":    {strings.Join(lines[:3], ""), key},
	} {
		if v := verifyAuditExport(c.file, c.key); v["valid"] != false {
			t.Fatalf("%s: %v", name, v)
		}
	}
}

func TestSecurityHelpers(t *testing.T) {
	if got := deviceOf(chrome); got != "Chrome on Windows" {
		t.Fatal(got)
	}
	if got := deviceOf("curl/8.4"); got != "curl" {
		t.Fatal(got)
	}
	if got := parseBlocklist(strings.NewReader("# Tor\n198.51.100.7\n10.0.0.0/8 ; private\nnot an ip\n2001:db8::/32\n")); len(got) != 3 {
		t.Fatalf("blocklist: %v", got)
	}
	rl := parseRouteLimits("POST /api/export* 10\n/api/search 300\n# comment 5\nbad\nGET /x zero")
	if len(rl) != 2 || rl[0].method != "POST" || rl[0].perMin != 10 || rl[1].method != "" || rl[1].like != "/api/search" {
		t.Fatalf("route limits: %+v", rl)
	}
	if d := distanceKm(51.5, -0.12, 40.71, -74.0); d < 5500 || d > 5600 {
		t.Fatalf("London to New York: %.0f km", d)
	}
}

func TestPortalAllowList(t *testing.T) {
	s := securityTestServer(t, map[string]string{"portal_allow_ips": "192.0.2.0/24"})
	h := s.Handler()
	for _, c := range []struct {
		method, path, ip string
		blocked          bool
	}{
		{"GET", "/api/session", "203.0.113.1:5000", true},
		{"GET", "/", "203.0.113.1:5000", true},
		{"GET", "/api/session", "192.0.2.9:5000", false},
		{"GET", "/api/session", "127.0.0.1:5000", false},
		{"GET", "/api/healthz", "203.0.113.1:5000", false},
		{"POST", "/v1/traces", "203.0.113.1:5000", false},
		{"GET", "/status.json", "203.0.113.1:5000", false},
	} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))
		r.RemoteAddr = c.ip
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		blocked := w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "address")
		if blocked != c.blocked {
			t.Fatalf("%s %s from %s: %d %s", c.method, c.path, c.ip, w.Code, w.Body)
		}
	}
}

func TestIdentityLinks(t *testing.T) {
	s := securityTestServer(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	// frank signs in by email; the app then knows him only by his id.
	addReqs(t, s,
		req{at: now.Add(-30 * time.Minute), method: "POST", path: "/api/login", status: 200, ip: "192.0.2.80", ua: chrome, login: "frank@x.io"},
		req{at: now.Add(-29 * time.Minute), path: "/api/orders", route: "/api/orders", status: 200, ip: "192.0.2.80", ua: chrome, handle: "u-123"},
	)
	runOK(t, s, now.Add(-25*time.Minute))
	// Later, on another browser signed in as u-123: mistyping his own
	// password is not someone else's account, zoe's is.
	rows := []req{{at: now.Add(-3 * time.Minute), path: "/api/orders", route: "/api/orders", status: 200, ip: "192.0.2.81", ua: firefoxLinux, handle: "u-123"}}
	for i := 0; i < 3; i++ {
		rows = append(rows,
			req{at: now.Add(-2 * time.Minute), method: "POST", path: "/api/login", status: 401, ip: "192.0.2.81", ua: firefoxLinux, login: "frank@x.io"},
			req{at: now.Add(-2 * time.Minute), method: "POST", path: "/api/login", status: 401, ip: "192.0.2.81", ua: firefoxLinux, login: "zoe@x.io"})
	}
	rows = append(rows, req{at: now.Add(-time.Minute), method: "PUT", path: "/api/me/email", route: "/api/me/email", status: 200, ip: "192.0.2.81", ua: firefoxLinux, handle: "u-123"})
	addReqs(t, s, rows...)
	if err := s.db.Exec(ctx, `INSERT INTO security_events (created_at, source, kind, severity, actor, ip, detail, count) VALUES (?, 'api', 'new_login_location', 'warning', 'frank@x.io', '192.0.2.81', '{}', 1)`,
		now.Add(-4*time.Minute).Format(tsLayout)); err != nil {
		t.Fatal(err)
	}
	runOK(t, s, now)
	got := findingsOf(t, s)
	if f := one(t, got, "login_as_other"); f.target != "zoe@x.io" {
		t.Fatalf("login as other: %+v", got["login_as_other"])
	}
	if f := one(t, got, "risky_account_change"); f.actor != "u-123" {
		t.Fatalf("risky change through the link: %+v", f)
	}
}

func TestWordLikes(t *testing.T) {
	got := wordLikes("password, /api/v1/sessions, /auth/*", nil)
	want := []string{"%password%", "/api/v1/sessions", "/auth/%"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%v, want %v", got, want)
	}
	if got := wordLikes("", []string{"otp"}); fmt.Sprint(got) != "[%otp%]" {
		t.Fatal(got)
	}
}
