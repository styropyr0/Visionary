package analytics

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
	"github.com/styropyr0/visionary/sdks/go/logrules"
)

func TestCompileLogQuery(t *testing.T) {
	sqlite, pg, my := sqlDialect{sdk.SQLite}, sqlDialect{sdk.Postgres}, sqlDialect{sdk.MySQL}
	cases := []struct {
		q     string
		d     sqlDialect
		where string
		args  []any
	}{
		{"", sqlite, "1=1", nil},
		{"timeout", sqlite, "message LIKE ? ESCAPE '!'", []any{"%timeout%"}},
		{"timeout", pg, "message ILIKE ? ESCAPE '!'", []any{"%timeout%"}},
		{`"conn refused" 50%`, sqlite, "(message LIKE ? ESCAPE '!' AND message LIKE ? ESCAPE '!')", []any{"%conn refused%", "%50!%%"}},
		{"level:error", sqlite, "severity = ?", []any{logrules.Error}},
		{"level:warn+", sqlite, "severity >= ?", []any{logrules.Warn}},
		{"level:>=warning", sqlite, "severity >= ?", []any{logrules.Warn}},
		{"service:api*", sqlite, "service LIKE ? ESCAPE '!'", []any{"api%"}},
		{"service:api OR service:web", sqlite, "(service = ? OR service = ?)", []any{"api", "web"}},
		{"-healthz", sqlite, "NOT COALESCE(message LIKE ? ESCAPE '!', 1=0)", []any{"%healthz%"}},
		{"NOT level:debug", sqlite, "NOT COALESCE(severity = ?, 1=0)", []any{logrules.Debug}},
		{"(a OR b) c", sqlite, "((message LIKE ? ESCAPE '!' OR message LIKE ? ESCAPE '!') AND message LIKE ? ESCAPE '!')", []any{"%a%", "%b%", "%c%"}},
		{"host:*", sqlite, "host IS NOT NULL", nil},
		{"order_id:42", sqlite, `(json_extract(attrs, ?) = ? OR CAST(json_extract(attrs, ?) AS REAL) = ?)`, []any{`$."order_id"`, "42", `$."order_id"`, 42.0}},
		{"attrs.user.plan:pro", my, `JSON_UNQUOTE(JSON_EXTRACT(attrs, ?)) = ?`, []any{`$."user.plan"`, "pro"}},
		{"http.status:>=500", pg, `CAST(NULLIF((CAST(attrs AS JSON) ->> ?), '') AS DOUBLE PRECISION) >= ?`, []any{"http.status", 500.0}},
		{"/time(d|o)ut/", pg, "message ~* ?", []any{"time(d|o)ut"}},
		{"/time(d|o)ut/", my, "REGEXP_LIKE(message, ?, 'i')", []any{"time(d|o)ut"}},
		{"/x/", sqlite, "message REGEXP ?", []any{"(?i)x"}},
		{"line:12", sqlite, "code_line = ?", []any{int64(12)}},
		{"a AND b", sqlite, "(message LIKE ? ESCAPE '!' AND message LIKE ? ESCAPE '!')", []any{"%a%", "%b%"}},
	}
	for _, c := range cases {
		got, err := compileLogQuery(c.q, c.d, false)
		if err != nil {
			t.Errorf("%q: %v", c.q, err)
			continue
		}
		if got.where != c.where {
			t.Errorf("%q (%s):\n got %s\nwant %s", c.q, c.d.d, got.where, c.where)
		}
		ga, _ := json.Marshal(got.args)
		wa, _ := json.Marshal(c.args)
		if string(ga) != string(wa) && !(len(got.args) == 0 && len(c.args) == 0) {
			t.Errorf("%q args: %s, want %s", c.q, ga, wa)
		}
	}
	for q, want := range map[string]string{
		`"open`:               "closing",
		`/[/`:                 "regular expression",
		`level:loud`:          "level is one of",
		`service:>=3`:         "isn't a number",
		`status:>=x`:          "needs a number",
		`(a OR b`:             "missing its )",
		`a)`:                  "unexpected )",
		`OR`:                  "both sides",
		`host:`:               "needs a value",
		strings.Repeat("a ", 70): "more than",
	} {
		if _, err := compileLogQuery(q, sqlite, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", q, err, want)
		}
	}
	if _, err := compileLogQuery("/x/", sqlite, true); err == nil || !strings.Contains(err.Error(), "aren't available") {
		t.Errorf("D1 regex: %v", err)
	}
}

// seedLogs stores lines through the pipeline at fixed recent times.
func seedLogs(t *testing.T, s *Server) time.Time {
	t.Helper()
	base := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	lines := []*logLine{
		{at: base, service: "api", host: "h1", sev: logrules.Info, message: "order placed", attrs: map[string]any{"order_id": 42, "plan": "pro", "card_last4": "4242"}, requestID: "r1"},
		{at: base.Add(time.Second), service: "api", host: "h1", sev: logrules.Error, message: "payment timeout after 30s", attrs: map[string]any{"order_id": 43, "http.status": 504}, requestID: "r2"},
		{at: base.Add(2 * time.Second), service: "api", host: "h1", sev: logrules.Warn, message: "retrying payment", attrs: map[string]any{"attempt": 2}},
		{at: base.Add(3 * time.Second), service: "web", host: "h2", sev: logrules.Info, message: "page rendered", attrs: map[string]any{"plan": "free"}},
		{at: base.Add(4 * time.Second), service: "secret-svc", host: "h3", sev: logrules.Error, message: "hidden from viewers"},
	}
	if _, err := s.ingestLogLines(context.Background(), lines); err != nil {
		t.Fatal(err)
	}
	return base
}

// getAs calls a GET endpoint as role ("admin", "viewer").
func getAs(t *testing.T, s *Server, role, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Name: "Una", Role: role}))
	w := httptest.NewRecorder()
	switch {
	case strings.HasPrefix(path, "/api/applogs/tail"):
		s.handleAppLogsTail(w, r)
	default:
		mux := http.NewServeMux()
		s.applogsAPIRoutes(mux)
		// Call the handler behind authed directly, with the session set.
		h, pattern := mux.Handler(r)
		_ = h
		_ = pattern
		r.SetPathValue("id", lastSegment(path))
		switch {
		case strings.HasSuffix(strings.Split(path, "?")[0], "/context"):
			r.SetPathValue("id", strings.Split(strings.TrimPrefix(path, "/api/applogs/"), "/")[0])
			s.handleAppLogContext(w, r)
		case strings.HasPrefix(path, "/api/applogs/histogram"):
			s.handleAppLogsHistogram(w, r)
		case strings.HasPrefix(path, "/api/applogs/fields"):
			s.handleAppLogsFields(w, r)
		case strings.HasPrefix(path, "/api/applogs/export"):
			s.handleAppLogsExport(w, r)
		case strings.HasPrefix(path, "/api/applogs/views"):
			s.handleLogViews(w, r)
		case strings.HasPrefix(path, "/api/applogs?") || path == "/api/applogs":
			s.handleAppLogs(w, r)
		default:
			s.handleAppLog(w, r)
		}
	}
	return w
}

func lastSegment(p string) string {
	p = strings.Split(p, "?")[0]
	return p[strings.LastIndexByte(p, '/')+1:]
}

func rangeQS(base time.Time, q string) string {
	v := url.Values{}
	v.Set("from", base.Add(-time.Hour).Format(time.RFC3339))
	v.Set("to", base.Add(time.Hour).Format(time.RFC3339))
	if q != "" {
		v.Set("q", q)
	}
	return v.Encode()
}

func TestAppLogsAPI(t *testing.T) {
	access, _ := json.Marshal(logServerSettings{Rate: defaultLogRate, Access: logAccessSettings{HiddenServices: []string{"secret-svc"}, MaskedFields: []string{"card_last4"}}})
	s := logsTestServer(t, map[string]string{settingLogServer: string(access)})
	base := seedLogs(t, s)

	list := func(role, q string) logResult {
		w := getAs(t, s, role, "/api/applogs?"+rangeQS(base, q))
		if w.Code != 200 {
			t.Fatalf("%s %q: %d %s", role, q, w.Code, w.Body.String())
		}
		var res logResult
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		return res
	}
	if got := list("admin", ""); len(got.Rows) != 5 || got.Rows[0][9] != "hidden from viewers" {
		t.Fatalf("all: %+v", got.Rows)
	}
	if got := list("viewer", ""); len(got.Rows) != 4 {
		t.Fatalf("viewer sees hidden service: %d rows", len(got.Rows))
	}
	for q, want := range map[string]int{
		"payment":                   2,
		"level:error":               2,
		"level:warn+ service:api":   2,
		"order_id:42":               1,
		"http.status:>=500":         1,
		"plan:pro OR plan:free":     2,
		"-payment service:api":      1,
		"/time(o|d)ut/":             1,
		`"payment timeout"`:         1,
		"request:r2":                1,
		"host:h* -host:h3":          4,
		"attempt:2 level:warn":      1,
		"level:fatal":               0,
	} {
		if got := list("admin", q); len(got.Rows) != want {
			t.Errorf("%q: %d rows, want %d", q, len(got.Rows), want)
		}
	}
	if got := list("admin", "payment timeout"); strings.Join(got.Terms, ",") != "payment,timeout" {
		t.Errorf("terms %v", got.Terms)
	}
	// A viewer sees masked fields as [hidden].
	if got := list("viewer", "order_id:42"); !strings.Contains(toString(got.Rows[0][10]), `"card_last4":"[hidden]"`) || len(got.Masked) != 1 {
		t.Errorf("mask: %v", got.Rows[0][10])
	}
	if w := getAs(t, s, "admin", "/api/applogs?"+rangeQS(base, "level:loud")); w.Code != 400 {
		t.Errorf("bad search: %d", w.Code)
	}

	// Paging with before=.
	first := list("admin", "")
	w := getAs(t, s, "admin", "/api/applogs?"+rangeQS(base, "")+"&limit=2")
	var page logResult
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if len(page.Rows) != 2 || !page.Truncated {
		t.Fatalf("page %+v", page.Result)
	}
	w = getAs(t, s, "admin", "/api/applogs?"+rangeQS(base, "")+"&limit=2&before="+toString(page.Rows[1][0]))
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if len(page.Rows) != 2 || toString(page.Rows[0][0]) != toString(first.Rows[2][0]) {
		t.Fatalf("next page %+v", page.Rows)
	}

	// Histogram: one bucket per level here.
	w = getAs(t, s, "admin", "/api/applogs/histogram?"+rangeQS(base, "service:api"))
	var hist Result
	_ = json.Unmarshal(w.Body.Bytes(), &hist)
	total := int64(0)
	for _, r := range hist.Rows {
		total += toInt(r[2])
	}
	if w.Code != 200 || total != 3 {
		t.Fatalf("histogram %d %s", w.Code, w.Body.String())
	}

	// Fields and top values.
	w = getAs(t, s, "admin", "/api/applogs/fields?"+rangeQS(base, ""))
	if !strings.Contains(w.Body.String(), `"name":"order_id"`) || !strings.Contains(w.Body.String(), `"name":"service"`) {
		t.Fatalf("fields %s", w.Body.String())
	}
	w = getAs(t, s, "viewer", "/api/applogs/fields?"+rangeQS(base, ""))
	if strings.Contains(w.Body.String(), "card_last4") {
		t.Fatalf("viewer sees masked field name %s", w.Body.String())
	}
	w = getAs(t, s, "admin", "/api/applogs/fields?"+rangeQS(base, "")+"&field=service")
	if !strings.Contains(w.Body.String(), `{"count":3,"value":"api"}`) {
		t.Fatalf("top values %s", w.Body.String())
	}
	w = getAs(t, s, "admin", "/api/applogs/fields?"+rangeQS(base, "")+"&field=plan")
	if !strings.Contains(w.Body.String(), `"value":"pro"`) {
		t.Fatalf("attr values %s", w.Body.String())
	}

	// One line, and its context.
	id := toString(first.Rows[3][0]) // "payment timeout"
	w = getAs(t, s, "admin", "/api/applogs/"+id)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "payment timeout") {
		t.Fatalf("line %d %s", w.Code, w.Body.String())
	}
	hiddenID := toString(first.Rows[0][0])
	if w := getAs(t, s, "viewer", "/api/applogs/"+hiddenID); w.Code != 404 {
		t.Fatalf("viewer reads hidden line: %d", w.Code)
	}
	w = getAs(t, s, "admin", "/api/applogs/"+id+"/context?n=1")
	var cx struct {
		Result Result `json:"result"`
		By     string `json:"by"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &cx)
	if cx.By != "host" || len(cx.Result.Rows) != 3 || cx.Result.Rows[0][9] != "order placed" || cx.Result.Rows[2][9] != "retrying payment" {
		t.Fatalf("context %s", w.Body.String())
	}

	// Export.
	w = getAs(t, s, "admin", "/api/applogs/export?"+rangeQS(base, "service:api")+"&format=csv")
	if lines := strings.Count(strings.TrimSpace(w.Body.String()), "\n"); lines != 3 || !strings.HasPrefix(w.Body.String(), "id,created_at") {
		t.Fatalf("csv %s", w.Body.String())
	}
	w = getAs(t, s, "admin", "/api/applogs/export?"+rangeQS(base, "order_id:42"))
	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(w.Body.String())), &line); err != nil || line["attrs"].(map[string]any)["plan"] != "pro" {
		t.Fatalf("ndjson %s", w.Body.String())
	}
}

func TestLogViews(t *testing.T) {
	s := logsTestServer(t, nil)
	do := func(role, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Name: "Una", Role: role}))
		w := httptest.NewRecorder()
		switch method {
		case "POST":
			s.handleCreateLogView(w, r)
		case "DELETE":
			r.SetPathValue("id", lastSegment(path))
			s.handleDeleteLogView(w, r)
		default:
			s.handleLogViews(w, r)
		}
		return w
	}
	if w := do("viewer", "POST", "/api/applogs/views", `{"name":"x","query":"a"}`); w.Code != 403 {
		t.Fatalf("viewer saves: %d", w.Code)
	}
	if w := do("editor", "POST", "/api/applogs/views", `{"name":"x","query":"level:loud"}`); w.Code != 400 {
		t.Fatalf("bad query saved: %d", w.Code)
	}
	w := do("editor", "POST", "/api/applogs/views", `{"name":"Payment errors","query":"payment level:error+","columns":["service","order_id"]}`)
	if w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body.String())
	}
	var v logView
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	w = do("viewer", "GET", "/api/applogs/views", "")
	if !strings.Contains(w.Body.String(), `"name":"Payment errors"`) || !strings.Contains(w.Body.String(), `"created_by":"Una"`) {
		t.Fatalf("list %s", w.Body.String())
	}
	if w := do("editor", "DELETE", "/api/applogs/views/"+toString(v.ID), ""); w.Code != 204 {
		t.Fatalf("delete %d", w.Code)
	}
}

func TestAppLogsTail(t *testing.T) {
	s := logsTestServer(t, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Role: "admin"}))
		s.handleAppLogsTail(w, r)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"?q=level:error", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	ready := false
	for sc.Scan() {
		line := sc.Text()
		if line == "event: ready" {
			ready = true
			_, _ = s.ingestLogLines(context.Background(), []*logLine{
				{service: "api", sev: logrules.Info, message: "not an error"},
				{service: "api", sev: logrules.Error, message: "live error"},
			})
		}
		if ready && strings.HasPrefix(line, "data: ") && strings.Contains(line, "live error") {
			if strings.Contains(line, "not an error") {
				t.Fatal("tail ignored the search")
			}
			return
		}
	}
	t.Fatal("no lines arrived")
}
