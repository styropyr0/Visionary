package analytics

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/styropyr0/visionary/internal/uptimecore"
)

func kindRunner(t *testing.T) (*Server, *uptimeRunner) {
	t.Helper()
	s := logsTestServer(t, nil)
	s.live().Config["uptime_allow_private"] = "true"
	return s, newUptimeRunner(s, s.uptimeSettings())
}

func normalized(t *testing.T, c UptimeCheck) UptimeCheck {
	t.Helper()
	if err := c.normalize(nil); err != nil {
		t.Fatal(err)
	}
	return c
}

func apiSite() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Password string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Password != "hunter22" {
			http.Error(w, `{"error":"bad password"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-Request-Id", "req-7")
		_, _ = io.WriteString(w, `{"data":{"token":"tok-abcdef123","user":{"id":42}},"roles":["admin","dev"]}`)
	})
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-abcdef123" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":`+r.PathValue("id")+`,"name":"Ann","orders":3}`)
	})
	return httptest.NewServer(mux)
}

func apiCheck(base string) UptimeCheck {
	return UptimeCheck{Name: "Sign in and read", Kind: "api", FollowRedirects: true, Secrets: map[string]string{"PASS": "hunter22"},
		API: &APIConfig{Requests: []APIRequest{
			{Name: "Login", Method: "POST", URL: base + "/login", BodyType: "json", Body: `{"password":"{{secret.PASS}}"}`,
				Extract: []APIExtract{{Var: "token", Source: "json", Path: "$.data.token"}, {Var: "uid", Source: "json", Path: "data.user.id"}, {Var: "rid", Source: "header", Path: "X-Request-Id"}},
				Assert: []APIAssertion{{Source: "status", Op: "in", Value: "200-299"}, {Source: "json", Path: "$.roles", Op: "contains", Value: "admin"},
					{Source: "json", Path: "$.data.user.id", Op: "gte", Value: "40"}, {Source: "time", Op: "lte", Value: "5000"}}},
			{Name: "Profile", Method: "GET", URL: base + "/users/{{var.uid}}", Headers: map[string]string{"Authorization": "Bearer {{var.token}}"},
				Assert: []APIAssertion{{Source: "json", Path: "$.name", Op: "equals", Value: "Ann"}, {Source: "body", Op: "contains", Value: "orders"},
					{Source: "header", Path: "Content-Type", Op: "contains", Value: "json"}}},
		}}}
}

func TestAPICheckNormalize(t *testing.T) {
	c := normalized(t, apiCheck("https://api.example.com"))
	if c.URL != "https://api.example.com/login" || c.TimeoutMs != 30000 || c.Method != "GET" {
		t.Fatalf("defaults: %+v", c)
	}
	m := c.masked()
	if m.Secrets["PASS"] != uptimeMask || m.API.Requests[1].Headers["Authorization"] != "Bearer {{var.token}}" {
		t.Fatalf("masking: %+v %+v", m.Secrets, m.API.Requests[1].Headers)
	}
	again := m
	if err := again.normalize(&c); err != nil || again.Secrets["PASS"] != "hunter22" {
		t.Fatalf("round trip: %v %v", err, again.Secrets)
	}
	bad := []func(c *UptimeCheck){
		func(c *UptimeCheck) { c.API.Requests = nil },
		func(c *UptimeCheck) { c.API.Requests[1].URL = "https://x/{{var.nope}}" },
		func(c *UptimeCheck) { c.API.Requests[0].Body = `{"p":"{{secret.NOPE}}"}` },
		func(c *UptimeCheck) { c.API.Requests[0].URL = "ftp://x" },
		func(c *UptimeCheck) { c.API.Requests[0].Body = `{bad json` },
		func(c *UptimeCheck) { c.API.Requests[0].Assert[0].Value = "700" },
		func(c *UptimeCheck) {
			c.API.Requests[0].Assert = []APIAssertion{{Source: "json", Path: "data..x", Op: "exists"}}
		},
		func(c *UptimeCheck) {
			c.API.Requests[0].Assert = []APIAssertion{{Source: "body", Op: "lt", Value: "3"}}
		},
		func(c *UptimeCheck) { c.API.Requests[0].Extract = []APIExtract{{Var: "bad name", Source: "status"}} },
		func(c *UptimeCheck) { c.API.Requests[0].Extract = []APIExtract{{Var: "x", Source: "regex", Path: "("}} },
	}
	for i, f := range bad {
		c := apiCheck("https://api.example.com")
		f(&c)
		if err := c.normalize(nil); err == nil {
			t.Errorf("case %d: accepted", i)
		}
	}
}

func TestAPICheckRun(t *testing.T) {
	site := apiSite()
	defer site.Close()
	s, u := kindRunner(t)
	ctx := context.Background()

	c := normalized(t, apiCheck(site.URL))
	res := u.probeWithRetry(ctx, c)
	if !res.OK || res.Status != 200 || res.Retried {
		t.Fatalf("passing check: %+v", res)
	}
	d := res.Detail.(uptimecore.APIDetail)
	if len(d.Requests) != 2 || d.Requests[0].State != "passed" || d.Requests[1].URL != site.URL+"/users/42" || d.Requests[0].Size == 0 {
		t.Fatalf("detail: %+v", d)
	}
	if err := s.saveUptimeCheck(ctx, &c); err != nil {
		t.Fatal(err)
	}
	if err := u.record(ctx, c, res); err != nil {
		t.Fatal(err)
	}

	bad := apiCheck(site.URL)
	bad.API.Requests[1].Assert = append(bad.API.Requests[1].Assert, APIAssertion{Source: "json", Path: "$.orders", Op: "gt", Value: "10"})
	bad.API.Requests = append(bad.API.Requests, APIRequest{Method: "GET", URL: site.URL + "/never"})
	bad = normalized(t, bad)
	res = uptimecore.ProbeKind(ctx, bad.core(), u.allowPrivate)
	d = res.Detail.(uptimecore.APIDetail)
	if res.OK || d.Requests[1].State != "failed" || d.Requests[2].State != "skipped" || d.Requests[1].Assertion != "$.orders > 10" || d.Requests[1].Got != "3" {
		t.Fatalf("failing check: %+v %+v", res, d)
	}
	if !strings.Contains(res.Error, "Profile") || !strings.Contains(res.Error, "$.orders > 10") {
		t.Errorf("error: %q", res.Error)
	}

	wrong := apiCheck(site.URL)
	wrong.Secrets["PASS"] = "nope-wrong"
	wrong.API.Requests[0].Body = `{"password":"{{secret.PASS}}","echo":"{{secret.PASS}}"}`
	wrong = normalized(t, wrong)
	res = uptimecore.ProbeKind(ctx, wrong.core(), u.allowPrivate)
	d = res.Detail.(uptimecore.APIDetail)
	if res.OK || res.Status != 401 || !strings.Contains(d.Requests[0].Excerpt, "bad password") {
		t.Fatalf("wrong password: %+v %+v", res, d)
	}
	joined, _ := json.Marshal(res)
	if strings.Contains(string(joined), "nope-wrong") || strings.Contains(string(joined), "tok-abcdef123") {
		t.Fatalf("secret recorded: %s", joined)
	}

	s.live().Config["uptime_allow_private"] = "false"
	guarded := newUptimeRunner(s, uptimeSettings{workers: 1})
	res = guarded.probeWithRetry(ctx, c)
	if res.OK || !res.Blocked() || res.Retried {
		t.Fatalf("guarded: %+v", res)
	}
}

func TestTCPCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.WriteString(conn, "SSH-2.0-Test\r\n")
				buf := make([]byte, 64)
				n, _ := conn.Read(buf)
				if strings.HasPrefix(string(buf[:n]), "PING") {
					_, _ = io.WriteString(conn, "+PONG\r\n")
				}
			}()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	_, u := kindRunner(t)
	ctx := context.Background()
	check := func(tcp TCPConfig) uptimeResult {
		return uptimecore.ProbeKind(ctx, normalized(t, UptimeCheck{Name: "tcp", Kind: "tcp", TimeoutMs: 2000, TCP: &tcp}).core(), u.allowPrivate)
	}
	if res := check(TCPConfig{Host: "127.0.0.1", Port: port}); !res.OK || res.ConnectMs == nil {
		t.Fatalf("connect: %+v", res)
	}
	if res := check(TCPConfig{Host: "127.0.0.1", Port: port, Expect: "SSH-2.0"}); !res.OK || !strings.Contains(res.Detail.(uptimecore.TCPDetail).Banner, "SSH-2.0-Test") {
		t.Fatalf("banner: %+v", res)
	}
	if res := check(TCPConfig{Host: "127.0.0.1", Port: port, Send: `PING\r\n`, Expect: "+PONG"}); !res.OK {
		t.Fatalf("send/expect: %+v", res)
	}
	if res := check(TCPConfig{Host: "127.0.0.1", Port: port, Expect: "220 smtp"}); res.OK || !strings.Contains(res.Error, "220 smtp") {
		t.Fatalf("wrong banner: %+v", res)
	}
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	cport := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	if res := check(TCPConfig{Host: "127.0.0.1", Port: cport}); res.OK {
		t.Fatalf("closed port: %+v", res)
	}
	u.s.live().Config["uptime_allow_private"] = "false"
	u = newUptimeRunner(u.s, uptimeSettings{workers: 1})
	if res := check(TCPConfig{Host: "localhost", Port: port}); res.OK || !res.Blocked() {
		t.Fatalf("guarded: %+v", res)
	}
}

func fakeDNS(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var p dnsmessage.Parser
			h, err := p.Start(buf[:n])
			if err != nil {
				continue
			}
			q, err := p.Question()
			if err != nil {
				continue
			}
			rh := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}
			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true})
			b.EnableCompression()
			_ = b.StartQuestions()
			_ = b.Question(q)
			_ = b.StartAnswers()
			if q.Name.String() != "example.test." {
				b = dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RCode: dnsmessage.RCodeNameError})
				_ = b.StartQuestions()
				_ = b.Question(q)
			} else {
				switch q.Type {
				case dnsmessage.TypeA:
					_ = b.AResource(rh, dnsmessage.AResource{A: [4]byte{192, 0, 2, 10}})
					_ = b.AResource(rh, dnsmessage.AResource{A: [4]byte{192, 0, 2, 11}})
				case dnsmessage.TypeTXT:
					_ = b.TXTResource(rh, dnsmessage.TXTResource{TXT: []string{"v=spf1 -all"}})
				case dnsmessage.TypeMX:
					_ = b.MXResource(rh, dnsmessage.MXResource{Pref: 10, MX: dnsmessage.MustNewName("mail.example.test.")})
				}
			}
			out, err := b.Finish()
			if err == nil {
				_, _ = pc.WriteTo(out, addr)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func TestDNSCheck(t *testing.T) {
	server := fakeDNS(t)
	_, u := kindRunner(t)
	ctx := context.Background()
	check := func(d DNSConfig) uptimeResult {
		d.Server = server
		return uptimecore.ProbeKind(ctx, normalized(t, UptimeCheck{Name: "dns", Kind: "dns", TimeoutMs: 3000, DNS: &d}).core(), u.allowPrivate)
	}
	res := check(DNSConfig{Name: "example.test", RecordType: "A"})
	if !res.OK || res.DNSMs == nil || strings.Join(res.Detail.(uptimecore.DNSDetail).Answers, ",") != "192.0.2.10,192.0.2.11" {
		t.Fatalf("A: %+v", res)
	}
	if res := check(DNSConfig{Name: "example.test", RecordType: "A", Match: "contains", Values: []string{"192.0.2.11"}}); !res.OK {
		t.Fatalf("contains: %+v", res)
	}
	if res := check(DNSConfig{Name: "example.test", RecordType: "A", Match: "equals", Values: []string{"192.0.2.10"}}); res.OK || !strings.Contains(res.Error, "unexpected answer") {
		t.Fatalf("equals: %+v", res)
	}
	if res := check(DNSConfig{Name: "example.test", RecordType: "TXT", Match: "equals", Values: []string{"v=spf1 -all"}}); !res.OK {
		t.Fatalf("TXT: %+v", res)
	}
	if res := check(DNSConfig{Name: "Example.test.", RecordType: "MX", Match: "contains", Values: []string{"mail.example.test"}}); !res.OK {
		t.Fatalf("MX: %+v", res)
	}
	if res := check(DNSConfig{Name: "missing.test", RecordType: "A"}); res.OK || !strings.Contains(res.Error, "no A record") {
		t.Fatalf("NXDOMAIN: %+v", res)
	}
	u.s.live().Config["uptime_allow_private"] = "false"
	u = newUptimeRunner(u.s, uptimeSettings{workers: 1})
	if res := check(DNSConfig{Name: "example.test", RecordType: "A"}); res.OK || !res.Blocked() {
		t.Fatalf("guarded server: %+v", res)
	}
}

func TestPingCheck(t *testing.T) {
	if !uptimecore.PingStatus().Available {
		t.Skip("ICMP ping is not available here: " + uptimecore.PingStatus().Error)
	}
	_, u := kindRunner(t)
	c := normalized(t, UptimeCheck{Name: "ping", Kind: "ping", TimeoutMs: 1000, Ping: &PingConfig{Host: "127.0.0.1", Packets: 3}})
	res := uptimecore.ProbeKind(context.Background(), c.core(), u.allowPrivate)
	d := res.Detail.(uptimecore.PingDetail)
	if !res.OK || d.Received != 3 || d.LossPct != 0 || d.AvgMs == nil || len(d.RTTs) != 3 {
		t.Fatalf("ping: %+v %+v", res, d)
	}
	u.s.live().Config["uptime_allow_private"] = "false"
	u = newUptimeRunner(u.s, uptimeSettings{workers: 1})
	if res := uptimecore.ProbeKind(context.Background(), c.core(), u.allowPrivate); res.OK || !res.Blocked() {
		t.Fatalf("guarded: %+v", res)
	}
}

func shortCert(t *testing.T, days int) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "short.test"}, DNSNames: []string{"short.test"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Duration(days)*24*time.Hour + time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func tlsListener(t *testing.T, cert tls.Certificate, maxVersion uint16) int {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MaxVersion: maxVersion})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = conn.(*tls.Conn).Handshake()
				conn.Close()
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestSSLCheck(t *testing.T) {
	_, u := kindRunner(t)
	ctx := context.Background()
	port := tlsListener(t, shortCert(t, 5), 0)
	check := func(s SSLConfig) uptimeResult {
		s.Host, s.Port = "127.0.0.1", cmpOr(s.Port, port)
		return uptimecore.ProbeKind(ctx, normalized(t, UptimeCheck{Name: "ssl", Kind: "ssl", TimeoutMs: 3000, SSL: &s}).core(), u.allowPrivate)
	}
	res := check(SSLConfig{})
	d := res.Detail.(uptimecore.SSLDetail)
	if res.OK || d.ChainValid || !d.HostnameValid || d.DaysLeft != 5 || !strings.Contains(res.Error, "not trusted") || res.CertExpiresAt == "" {
		t.Fatalf("self-signed: %+v %+v", res, d)
	}
	if res := check(SSLConfig{IgnoreChain: true}); !res.OK || !res.Degraded {
		t.Fatalf("warn under 14 days: %+v", res)
	}
	if res := check(SSLConfig{IgnoreChain: true, WarnDays: 3}); !res.OK || res.Degraded {
		t.Fatalf("no warning: %+v", res)
	}
	if res := check(SSLConfig{IgnoreChain: true, FailDays: 7}); res.OK || !strings.Contains(res.Error, "expires in 5 days") {
		t.Fatalf("fail under 7 days: %+v", res)
	}
	if res := check(SSLConfig{IgnoreChain: true, ServerName: "other.test"}); res.OK || !strings.Contains(res.Error, "not valid for other.test") {
		t.Fatalf("hostname: %+v", res)
	}
	old := tlsListener(t, shortCert(t, 30), tls.VersionTLS12)
	if res := check(SSLConfig{Port: old, IgnoreChain: true, MinTLS: "1.3"}); res.OK || !strings.Contains(res.Error, "TLS 1.2") {
		t.Fatalf("min TLS: %+v", res)
	}
	expired := tlsListener(t, shortCert(t, -3), 0)
	if res := check(SSLConfig{Port: expired, IgnoreChain: true}); res.OK || !strings.Contains(res.Error, "expired") {
		t.Fatalf("expired: %+v", res)
	}
}

func cmpOr(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

func TestCron(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	cases := []struct{ expr, from, want string }{
		{"*/15 * * * *", "2026-03-01T10:07:30Z", "2026-03-01T10:15:00Z"},
		{"0 3 * * *", "2026-03-01T03:00:00Z", "2026-03-02T03:00:00Z"},
		{"30 9 * * MON-FRI", "2026-10-30T10:00:00Z", "2026-11-02T09:30:00Z"},
		{"0 0 1 */3 *", "2026-02-10T00:00:00Z", "2026-04-01T00:00:00Z"},
		{"@hourly", "2026-01-01T23:59:00Z", "2026-01-02T00:00:00Z"},
		{"0 12 13 * 5", "2026-11-01T00:00:00Z", "2026-11-06T12:00:00Z"},
	}
	for _, c := range cases {
		spec, err := parseCron(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := spec.next(at(c.from)); !got.Equal(at(c.want)) {
			t.Errorf("%s after %s = %s, want %s", c.expr, c.from, got.Format(time.RFC3339), c.want)
		}
	}
	for _, bad := range []string{"* * * *", "60 * * * *", "* * * * MON-", "5-1 * * * *", "*/0 * * * *"} {
		if _, err := parseCron(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	h := HeartbeatConfig{Cron: "0 2 * * *", Timezone: "America/New_York"}
	if err := h.normalize(nil); err != nil {
		t.Skip("no time zone data: ", err)
	}
	if got := h.expectedAfter(at("2026-07-01T12:00:00Z")); !got.Equal(at("2026-07-02T06:00:00Z")) {
		t.Errorf("in New York: %s", got)
	}
}

func TestHeartbeatVerdict(t *testing.T) {
	h := HeartbeatConfig{PeriodS: 3600, GraceS: 300}
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return created.Add(time.Duration(m) * time.Minute) }
	cases := []struct {
		name    string
		p       heartbeatPings
		now     time.Time
		ok      bool
		waiting bool
		reason  string
	}{
		{"new", heartbeatPings{}, at(30), true, true, ""},
		{"never pinged", heartbeatPings{}, at(66), false, false, "no ping yet"},
		{"on time", heartbeatPings{success: at(60)}, at(100), true, false, ""},
		{"within grace", heartbeatPings{success: at(60)}, at(124), true, false, ""},
		{"late", heartbeatPings{success: at(60)}, at(126), false, false, "no ping since"},
		{"failed", heartbeatPings{success: at(60), fail: at(70), failCode: new(int64(3))}, at(71), false, false, "exit code 3"},
		{"recovered", heartbeatPings{success: at(80), fail: at(70)}, at(81), true, false, ""},
		{"stuck", heartbeatPings{success: at(60), start: at(100)}, at(106), false, false, "did not report success"},
		{"running", heartbeatPings{success: at(60), start: at(100)}, at(103), true, false, ""},
	}
	for _, c := range cases {
		v := h.verdict(c.p, created, c.now)
		if v.ok != c.ok || v.waiting != c.waiting || !strings.Contains(v.reason, c.reason) {
			t.Errorf("%s: %+v", c.name, v)
		}
	}
}

func TestHeartbeatEndpoint(t *testing.T) {
	s, u := kindRunner(t)
	ctx := context.Background()
	c := normalized(t, UptimeCheck{Name: "Nightly backup", Kind: "heartbeat", Enabled: true, Heartbeat: &HeartbeatConfig{PeriodS: 3600, GraceS: 60}})
	if err := s.saveUptimeCheck(ctx, &c); err != nil {
		t.Fatal(err)
	}
	stored, err := s.getUptimeCheck(ctx, c.ID)
	if err != nil || stored.Heartbeat.Token != c.Heartbeat.Token || stored.masked().Heartbeat.Token != "" {
		t.Fatalf("token: %v %+v", err, stored.Heartbeat)
	}
	mux := http.NewServeMux()
	s.uptimeKindsRoutes(mux)
	ping := func(method, path, body string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec.Code
	}
	base := "/api/heartbeat/" + c.Heartbeat.Token
	if res := u.probeWithRetry(ctx, stored); res.Skip == "" {
		t.Fatalf("before any ping: %+v", res)
	}
	if code := ping("GET", base+"/start", ""); code != 200 {
		t.Fatalf("start: %d", code)
	}
	time.Sleep(20 * time.Millisecond)
	if code := ping("POST", base, "backup ok"); code != 200 {
		t.Fatalf("success: %d", code)
	}
	p, err := s.lastHeartbeats(ctx, c.ID)
	if err != nil || p.success.IsZero() || p.durationMs == nil || *p.durationMs < 15 {
		t.Fatalf("pings: %v %+v", err, p)
	}
	if code := ping("POST", base+"/2", "disk full"); code != 200 {
		t.Fatalf("exit code: %d", code)
	}
	res := s.heartbeatResult(ctx, stored, time.Now())
	if res.OK || !strings.Contains(res.Error, "exit code 2") || !strings.Contains(res.Error, "disk full") {
		t.Fatalf("after failure: %+v", res)
	}
	rows, _ := s.db.Query(ctx, `SELECT ok FROM uptime_results WHERE check_id = ? ORDER BY created_at`, c.ID)
	if len(rows.Rows) != 2 || toInt(rows.Rows[0][0]) != 1 || toInt(rows.Rows[1][0]) != 0 {
		t.Fatalf("results: %v", rows.Rows)
	}
	for path, want := range map[string]int{
		"/api/heartbeat/" + strings.Repeat("0", 48): 404, "/api/heartbeat/short": 404, base + "/nope": 404, base + "/300": 404,
	} {
		if code := ping("GET", path, ""); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	if code := ping("DELETE", base, ""); code != 405 {
		t.Errorf("DELETE: %d", code)
	}
	limited := 0
	for range 20 {
		if ping("HEAD", base, "") == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Error("pings were not rate limited")
	}
	s.deleteKindData(ctx, c.ID)
	if code := ping("GET", "/api/heartbeat/"+c.Heartbeat.Token+"/0", ""); code != 404 && code != 429 {
		t.Errorf("deleted token: %d", code)
	}
}

func TestKindNormalize(t *testing.T) {
	bad := []UptimeCheck{
		{Name: "x", Kind: "tcp", TCP: &TCPConfig{Host: "db.example.com", Port: 70000}},
		{Name: "x", Kind: "tcp", TCP: &TCPConfig{Host: "bad host", Port: 22}},
		{Name: "x", Kind: "tcp"},
		{Name: "x", Kind: "dns", DNS: &DNSConfig{Name: "example.com", RecordType: "SRV"}},
		{Name: "x", Kind: "dns", DNS: &DNSConfig{Name: "example.com", Match: "equals"}},
		{Name: "x", Kind: "ping", Ping: &PingConfig{Host: "example.com", Packets: 50}},
		{Name: "x", Kind: "ping", IntervalS: 30, TimeoutMs: 5000, Ping: &PingConfig{Host: "example.com", Packets: 10}},
		{Name: "x", Kind: "ssl", SSL: &SSLConfig{Host: "example.com", WarnDays: 3, FailDays: 7}},
		{Name: "x", Kind: "ssl", SSL: &SSLConfig{Host: "example.com", MinTLS: "1.4"}},
		{Name: "x", Kind: "heartbeat", Heartbeat: &HeartbeatConfig{PeriodS: 10}},
		{Name: "x", Kind: "heartbeat", Heartbeat: &HeartbeatConfig{PeriodS: 600, Cron: "* * * * *"}},
		{Name: "x", Kind: "heartbeat", Heartbeat: &HeartbeatConfig{Cron: "0 3 * * *", Timezone: "Mars/Olympus"}},
	}
	for i, c := range bad {
		if err := c.normalize(nil); err == nil {
			t.Errorf("case %d (%s): accepted", i, c.Kind)
		}
	}
	c := normalized(t, UptimeCheck{Name: "x", Kind: "dns", Headers: map[string]string{"a": "b"}, DNS: &DNSConfig{Name: "Example.COM."}, TCP: &TCPConfig{Host: "x"}})
	if c.DNS.RecordType != "A" || c.TCP != nil || len(c.Headers) != 0 || c.URL != "dns://example.com?type=A" {
		t.Fatalf("dns defaults: %+v", c)
	}
	c = normalized(t, UptimeCheck{Name: "x", Kind: "ssl", SSL: &SSLConfig{Host: "[::1]"}})
	if c.SSL.Port != 443 || c.SSL.WarnDays != 14 || c.URL != "ssl://[::1]:443" {
		t.Fatalf("ssl defaults: %+v %s", c.SSL, c.URL)
	}
	hb := normalized(t, UptimeCheck{Name: "x", Kind: "heartbeat", Heartbeat: &HeartbeatConfig{PeriodS: 600}})
	if hb.IntervalS != 60 || len(hb.Heartbeat.Token) != 48 {
		t.Fatalf("heartbeat: %+v", hb.Heartbeat)
	}
	again := hb.masked()
	if err := again.normalize(&hb); err != nil || again.Heartbeat.Token != hb.Heartbeat.Token {
		t.Fatalf("token kept: %v", err)
	}
	if !uptimecore.BlockedAddr(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("guard")
	}
}
