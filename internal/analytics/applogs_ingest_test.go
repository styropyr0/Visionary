package analytics

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

// logsTestServer is a server on a fresh SQLite file with an ingest key k1
// and the given Settings → Logs values (logs.* and logserver.config).
func logsTestServer(t *testing.T, settings map[string]string) *Server {
	t.Helper()
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "logs.db"))
	s, err := New(Config{DatabaseURL: "sqlite:///" + path}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.db.Close() })
	// Ingestion writes through the OTLP state's own pool; close it first
	// so the file can be removed.
	t.Cleanup(func() {
		if v, ok := otlpStates.LoadAndDelete(s); ok && v.(*otlpState).sql != nil {
			_ = v.(*otlpState).sql.Close()
		}
	})
	l := defaultLiveSettings()
	l.Config["ingest_keys"] = "k1"
	m := map[string][2]string{}
	for k, v := range settings {
		m[k] = [2]string{v, ""}
	}
	l.Logs, l.LogServer = parseLogSettings(m)
	s.liveSet.p.Store(l)
	logIngests.Delete(s)
	return s
}

// logRows reads app_logs as maps, oldest first.
func logRows(t *testing.T, s *Server) []map[string]any {
	t.Helper()
	res, err := s.db.Query(context.Background(), `SELECT * FROM app_logs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, r := range res.Rows {
		m := map[string]any{}
		for i, c := range res.Columns {
			m[c] = r[i]
		}
		out = append(out, m)
	}
	return out
}

func attrsOf(t *testing.T, row map[string]any) map[string]any {
	t.Helper()
	m := map[string]any{}
	if a := toString(row["attrs"]); a != "" {
		if err := json.Unmarshal([]byte(a), &m); err != nil {
			t.Fatalf("attrs %q: %v", a, err)
		}
	}
	return m
}

func ingestPost(t *testing.T, s *Server, path, ctype, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

var withKey = map[string]string{"Authorization": "Bearer k1"}

func TestIngestNativeJSON(t *testing.T) {
	s := logsTestServer(t, nil)
	body := `[
	 {"message":"order placed","level":"info","service":"shop","time":"2026-10-27T09:00:00.123Z","order_id":42,"user":{"id":"u1","plan":"pro"},"request_id":"r1","password":"hunter2"},
	 {"msg":"slow query","lvl":"WARNING","ts":1792141200.5,"logger":"db","trace.id":"abc"},
	 {"message":"boom","level":50,"caller":"pkg/x.go:12"}
	]`
	w := ingestPost(t, s, "/api/ingest/logs?service=fallback&host=h1", "application/json", body, withKey)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"accepted":3`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	rows := logRows(t, s)
	if len(rows) != 3 {
		t.Fatalf("%d rows", len(rows))
	}
	r := rows[0]
	if r["message"] != "order placed" || r["service"] != "shop" || r["level"] != "info" || toString(r["created_at"]) != "2026-10-27T09:00:00.123Z" ||
		r["request_id"] != "r1" || r["user_id"] != "u1" || r["host"] != "h1" || r["source"] != "http" {
		t.Fatalf("row 0 %+v", r)
	}
	a := attrsOf(t, r)
	if a["order_id"] != float64(42) || a["user.plan"] != "pro" || a["password"] != "[REDACTED]" {
		t.Fatalf("attrs %+v", a)
	}
	if rows[1]["level"] != "warn" || rows[1]["logger"] != "db" || rows[1]["trace_id"] != "abc" || rows[1]["service"] != "fallback" {
		t.Fatalf("row 1 %+v", rows[1])
	}
	if rows[2]["level"] != "error" || rows[2]["code_file"] != "pkg/x.go" || toInt(rows[2]["code_line"]) != 12 {
		t.Fatalf("row 2 %+v", rows[2])
	}
}

func TestIngestTextAndNDJSON(t *testing.T) {
	s := logsTestServer(t, nil)
	body := "2026-10-27 10:00:01 ERROR payment failed token=abc123\n" +
		`{"message":"structured","level":"debug"}` + "\n" +
		"\n" +
		`time=2026-10-27T10:00:02Z level=warn msg="disk almost full" mount=/data pct=93` + "\n" +
		`203.0.113.9 - - [27/Oct/2026:10:00:03 +0000] "GET /api/orders/7 HTTP/1.1" 502 120 "-" "curl/8"` + "\n" +
		"just a line\n"
	w := ingestPost(t, s, "/api/ingest/logs?service=api", "text/plain", body, map[string]string{"x-visionary-key": "k1"})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	rows := logRows(t, s)
	if len(rows) != 4 {
		// debug is below the default level and dropped.
		t.Fatalf("%d rows: %+v", len(rows), rows)
	}
	if rows[0]["level"] != "error" || toString(rows[0]["message"]) != "2026-10-27 10:00:01 ERROR payment failed token=[REDACTED]" {
		t.Fatalf("text %+v", rows[0])
	}
	if rows[1]["level"] != "warn" || rows[1]["message"] != "disk almost full" || attrsOf(t, rows[1])["pct"] != "93" {
		t.Fatalf("logfmt %+v", rows[1])
	}
	acc := attrsOf(t, rows[2])
	if rows[2]["level"] != "error" || acc["http.status"] != float64(502) || acc["http.path"] != "/api/orders/7" || acc["client.ip"] != "203.0.113.9" {
		t.Fatalf("access log %+v %+v", rows[2], acc)
	}
	if rows[3]["message"] != "just a line" || rows[3]["level"] != "info" {
		t.Fatalf("plain %+v", rows[3])
	}
}

func TestIngestAuth(t *testing.T) {
	s := logsTestServer(t, nil)
	if w := ingestPost(t, s, "/api/ingest/logs", "text/plain", "x", nil); w.Code != 401 {
		t.Fatalf("no key: %d", w.Code)
	}
	if w := ingestPost(t, s, "/api/ingest/logs", "text/plain", "x", map[string]string{"Authorization": "Bearer nope"}); w.Code != 401 {
		t.Fatalf("bad key: %d", w.Code)
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("elastic:k1"))
	if w := ingestPost(t, s, "/api/ingest/logs", "text/plain", "x", map[string]string{"Authorization": basic}); w.Code != 200 {
		t.Fatalf("basic: %d", w.Code)
	}
	apiKey := "ApiKey " + base64.StdEncoding.EncodeToString([]byte("id:k1"))
	if w := ingestPost(t, s, "/api/ingest/logs", "text/plain", "x", map[string]string{"Authorization": apiKey}); w.Code != 200 {
		t.Fatalf("api key: %d", w.Code)
	}
	if w := ingestPost(t, s, "/api/ingest/logs?key=k1", "text/plain", "x", nil); w.Code != 200 {
		t.Fatalf("query key: %d", w.Code)
	}
}

func TestIngestElasticsearchBulk(t *testing.T) {
	s := logsTestServer(t, nil)
	body := `{"index":{"_index":"logs-app"}}
{"@timestamp":"2026-10-27T11:00:00.000Z","message":"user signed in","log":{"level":"info","logger":"auth"},"service":{"name":"web"},"host":{"name":"vm-1"},"container":{"name":"web-1"},"trace":{"id":"t1"},"kubernetes":{"pod":{"name":"web-abc"}}}
{"create":{}}
{"log":"container stdout line\n","stream":"stdout","time":"2026-10-27T11:00:01Z"}
{"delete":{"_id":"1"}}
`
	w := ingestPost(t, s, "/api/ingest/logs/logs-app/_bulk", "application/x-ndjson", body, withKey)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Errors bool             `json:"errors"`
		Items  []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Errors || len(resp.Items) != 3 {
		t.Fatalf("response %s", w.Body.String())
	}
	rows := logRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("%d rows", len(rows))
	}
	r := rows[0]
	if r["message"] != "user signed in" || r["service"] != "web" || r["host"] != "vm-1" || r["container"] != "web-1" || r["logger"] != "auth" || r["trace_id"] != "t1" || r["source"] != "bulk" {
		t.Fatalf("ecs %+v", r)
	}
	if attrsOf(t, r)["kubernetes.pod.name"] != "web-abc" {
		t.Fatalf("attrs %+v", attrsOf(t, r))
	}
	if rows[1]["message"] != "container stdout line" || attrsOf(t, rows[1])["stream"] != "stdout" {
		t.Fatalf("docker json %+v", rows[1])
	}
	// The version check shippers make first.
	req := httptest.NewRequest("GET", "/api/ingest/logs", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"number":"8.11.0"`) {
		t.Fatalf("info %d %s", rec.Code, rec.Body.String())
	}
}

func TestIngestLoki(t *testing.T) {
	s := logsTestServer(t, nil)
	ns := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC).UnixNano()
	body := fmt.Sprintf(`{"streams":[{"stream":{"job":"worker","level":"error","env":"prod"},"values":[["%d","job failed: timeout",{"trace_id":"t9"}],["%d","{\"msg\":\"json inside\",\"n\":3}"]]}]}`, ns, ns+1)
	w := ingestPost(t, s, "/loki/api/v1/push", "application/json", body, withKey)
	if w.Code != 204 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	rows := logRows(t, s)
	if len(rows) != 2 || rows[0]["service"] != "worker" || rows[0]["level"] != "error" || rows[0]["trace_id"] != "t9" || attrsOf(t, rows[0])["env"] != "prod" {
		t.Fatalf("rows %+v", rows)
	}
	if toString(rows[0]["created_at"]) != "2026-10-27T12:00:00.000Z" {
		t.Fatalf("time %v", rows[0]["created_at"])
	}
	if rows[1]["message"] != "json inside" || attrsOf(t, rows[1])["n"] != float64(3) {
		t.Fatalf("json line %+v", rows[1])
	}
	if w := ingestPost(t, s, "/loki/api/v1/push", "application/x-protobuf", "x", withKey); w.Code != 400 {
		t.Fatalf("protobuf: %d", w.Code)
	}
}

func TestIngestOTLPLogs(t *testing.T) {
	s := logsTestServer(t, nil)
	s.liveSet.p.Load().Config["errors"] = "true"
	body := `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"checkout"}},{"key":"host.name","value":{"stringValue":"node-7"}}]},
	 "scopeLogs":[{"scope":{"name":"checkout.payments"},"logRecords":[
	  {"timeUnixNano":"1792141200000000000","severityNumber":17,"body":{"stringValue":"card declined"},"traceId":"4bf92f3577b34da6a3ce929d0e0e4736",
	   "attributes":[{"key":"exception.type","value":{"stringValue":"CardError"}},{"key":"code.filepath","value":{"stringValue":"pay.py"}},{"key":"code.lineno","value":{"intValue":"88"}},{"key":"amount","value":{"doubleValue":12.5}}]},
	  {"timeUnixNano":"1792141201000000000","severityText":"INFO","body":{"kvlistValue":{"values":[{"key":"msg","value":{"stringValue":"from a map"}},{"key":"k","value":{"stringValue":"v"}}]}}},
	  {"timeUnixNano":"1792141202000000000","severityNumber":5,"body":{"stringValue":"debug noise"}}
	 ]}]}]}`
	w := ingestPost(t, s, "/v1/logs", "application/json", body, withKey)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	rows := logRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("%d rows: %+v", len(rows), rows)
	}
	r := rows[0]
	if r["service"] != "checkout" || r["host"] != "node-7" || r["level"] != "error" || r["message"] != "card declined" || r["logger"] != "checkout.payments" ||
		r["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" || r["code_file"] != "pay.py" || toInt(r["code_line"]) != 88 || r["source"] != "otlp" {
		t.Fatalf("otlp %+v", r)
	}
	if a := attrsOf(t, r); a["amount"] != 12.5 || a["exception.type"] != "CardError" {
		t.Fatalf("attrs %+v", a)
	}
	if rows[1]["message"] != "from a map" || attrsOf(t, rows[1])["k"] != "v" {
		t.Fatalf("map body %+v", rows[1])
	}
	// The exception still becomes an error, as before.
	res, err := s.db.Query(context.Background(), `SELECT type FROM errors`)
	if err != nil || len(res.Rows) != 1 || res.Rows[0][0] != "CardError" {
		t.Fatalf("errors %+v %v", res, err)
	}
}

func TestIngestSettings(t *testing.T) {
	drops, _ := json.Marshal([]map[string]string{{"contains": "healthz"}})
	conf, _ := json.Marshal(logServerSettings{Rate: 3, Redact: []string{"ssn"}, Rules: []parseRule{
		{Name: "payments", Service: "pay", Pattern: `%{TIMESTAMP_ISO8601:time} \[%{LOGLEVEL:level}\] order=%{INT:order} %{GREEDYDATA:message}`},
	}})
	s := logsTestServer(t, map[string]string{"logs.level": "warn", "logs.drop": string(drops), settingLogServer: string(conf)})
	lines := []*logLine{
		{service: "pay", message: "2026-10-27T09:00:00Z [ERROR] order=77 gateway timeout"},
		{service: "pay", message: "GET /healthz error: fine"},
		{service: "pay", sev: logrules.Info, message: "info is below warn"},
		{service: "pay", sev: logrules.Warn, message: "w1", attrs: map[string]any{"ssn": "123"}},
		{service: "pay", sev: logrules.Warn, message: "w2"},
		{service: "pay", sev: logrules.Warn, message: "over the cap"},
		{service: "pay", sev: logrules.Error, message: "errors pass the cap"},
		{service: "other", sev: logrules.Warn, message: "own cap"},
	}
	if _, err := s.ingestLogLines(context.Background(), lines); err != nil {
		t.Fatal(err)
	}
	rows := logRows(t, s)
	var got []string
	for _, r := range rows {
		got = append(got, toString(r["message"]))
	}
	if strings.Join(got, "|") != "gateway timeout|w1|w2|errors pass the cap|own cap" {
		t.Fatalf("got %q", got)
	}
	a := attrsOf(t, rows[0])
	if rows[0]["level"] != "error" || a["order"] != float64(77) || a["parsed_by"] != "payments" || toString(rows[0]["created_at"]) != "2026-10-27T09:00:00.000Z" {
		t.Fatalf("rule %+v %+v", rows[0], a)
	}
	if attrsOf(t, rows[1])["ssn"] != "[REDACTED]" {
		t.Fatalf("redact %+v", attrsOf(t, rows[1]))
	}
}

func TestParseRuleErrors(t *testing.T) {
	for pattern, want := range map[string]string{
		"%{NOPE:x}":   "unknown placeholder",
		"(unclosed":   "not a valid expression",
		"no fields":   "no named fields",
		"%{WORD:ok}x": "",
	} {
		r := parseRule{Pattern: pattern}
		err := r.compile()
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%q: %v", pattern, err)
		}
	}
}

func TestParseSyslog(t *testing.T) {
	l := parseSyslog(`<165>1 2026-10-28T10:00:00.000Z web-1 nginx 1234 ID47 [exampleSDKey@32473 iut="3" eventSource="App\"lication"] An application event`, "")
	if l.sev != logrules.Info || l.host != "web-1" || l.service != "nginx" || l.message != "An application event" ||
		l.attrs["syslog.facility"] != "local4" || l.attrs["exampleSDKey@32473.eventSource"] != `App"lication` || l.attrs["syslog.msgid"] != "ID47" {
		t.Fatalf("5424 %+v", l)
	}
	l = parseSyslog(`<11>Oct 28 10:00:00 db-2 postgres[999]: FATAL: too many connections`, "")
	if l.sev != logrules.Error || l.host != "db-2" || l.service != "postgres" || l.attrs["syslog.procid"] != "999" || l.message != "FATAL: too many connections" {
		t.Fatalf("3164 %+v", l)
	}
	l = parseSyslog(`<15>no header here`, "legacy")
	if l.sev != logrules.Debug || l.message != "no header here" || l.service != "legacy" {
		t.Fatalf("bare %+v", l)
	}
	if parseSyslog("  ", "") != nil {
		t.Fatal("blank")
	}
	r := bufio.NewReader(strings.NewReader("12 <13>1 - x y\n<14>plain line\n"))
	for _, want := range []string{"<13>1 - x y\n", "<14>plain line"} {
		m, _ := readSyslogFrame(r)
		if strings.TrimSpace(m) != strings.TrimSpace(want) {
			t.Fatalf("frame %q, want %q", m, want)
		}
	}
}

func TestSyslogListener(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Find a free port, then let the receiver bind it.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	s.syslogApply(ctx, addr)
	if got, errText := s.syslogStatus(); got != addr || errText != "" {
		t.Fatalf("status %q %q", got, errText)
	}
	u, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(u, "<12>Oct 28 10:00:00 host-a app-a: udp warning")
	u.Close()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	msg := "<11>1 2026-10-28T10:00:00Z host-b app-b - - - tcp error"
	fmt.Fprintf(c, "%d %s", len(msg), msg)
	c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rn := s.syslog()
		rn.mu.Lock()
		n := len(rn.batch)
		rn.mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d lines received", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.syslogApply(ctx, "")
	if got, _ := s.syslogStatus(); got != "" {
		t.Fatalf("still bound to %q", got)
	}
}

func TestLogSettingsFromLive(t *testing.T) {
	s := logsTestServer(t, map[string]string{"logs.enabled": "false"})
	n, err := s.ingestLogLines(context.Background(), []*logLine{{message: "x"}})
	if err != nil || n != 0 || len(logRows(t, s)) != 0 {
		t.Fatalf("disabled: %d %v", n, err)
	}
	var _ http.Handler = s.Handler()
}
