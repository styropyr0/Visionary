package visionary

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

// logAgent is an Agent with only log capture, writing to a fake store; its
// queued lines are read with queued.
func logAgent(t *testing.T, rows [][]string) *Agent {
	t.Helper()
	a := &Agent{cfg: Config{Logger: slog.Default(), Requests: RequestsConfig{Enabled: true}}, db: &fakeStore{data: rows},
		ident: identity{service: "api", instance: "i-1", version: "v2"}}
	w := newWriter(RequestsConfig{QueueSize: 1000}, false, a.ident, a.db, a.cfg.Logger)
	w.logsOnly, w.logSecrets = true, logrules.SecretKeys(nil)
	a.writer = newWriter(RequestsConfig{}, true, a.ident, a.db, a.cfg.Logger)
	a.logs = newLogger(a, w, sevInfo)
	if rows != nil {
		a.logs.set.Store(logrules.Parse(rows, sevInfo))
	}
	return a
}

func queued(a *Agent) []*logRecord {
	var out []*logRecord
	for {
		select {
		case r := <-a.logs.w.queue:
			out = append(out, r.(*logRecord))
		default:
			return out
		}
	}
}

// messages lists the lines in the order they were logged, which is the
// order they are stored in (held lines are queued later).
func messages(recs []*logRecord) string {
	slices.SortStableFunc(recs, func(a, b *logRecord) int { return a.at.Compare(b.at) })
	var m []string
	for _, r := range recs {
		m = append(m, r.message)
	}
	return strings.Join(m, ",")
}

// serve runs one request through Middleware; the handler logs with the
// request's context and answers status after taking took.
func serve(a *Agent, path string, status int, took time.Duration, user string, log func(ctx context.Context, l *slog.Logger)) {
	l := slog.New(LogHandler(nil))
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user != "" {
			SetUser(r.Context(), User{ID: user})
		}
		SetRoute(r.Context(), path)
		log(r.Context(), l)
		time.Sleep(took)
		w.WriteHeader(status)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
}

// threeLines logs a little apart, so the lines' times order them even on
// clocks with a coarse tick (Windows).
func threeLines(ctx context.Context, l *slog.Logger) {
	l.DebugContext(ctx, "debug line")
	time.Sleep(2 * time.Millisecond)
	l.InfoContext(ctx, "info line")
	time.Sleep(2 * time.Millisecond)
	l.WarnContext(ctx, "warn line")
}

func TestLogsKeepDebugOnlyForFailedRequests(t *testing.T) {
	a := logAgent(t, nil)
	serve(a, "/ok", 200, 0, "", threeLines)
	if got := messages(queued(a)); got != "info line,warn line" {
		t.Fatalf("200: %q", got)
	}
	serve(a, "/fail", 500, 0, "", threeLines)
	got := queued(a)
	// info and warn are written at once; debug when the request has failed.
	if messages(got) != "debug line,info line,warn line" {
		t.Fatalf("500: %q", messages(got))
	}
	for _, r := range got {
		if r.requestID == "" {
			t.Fatalf("line without request id: %+v", r)
		}
	}
}

func TestLogsKeepDebugForSlowRequests(t *testing.T) {
	a := logAgent(t, [][]string{{"logs.slow_ms", "20"}})
	serve(a, "/slow", 200, 30*time.Millisecond, "", threeLines)
	if got := messages(queued(a)); got != "debug line,info line,warn line" {
		t.Fatalf("slow: %q", got)
	}
	a = logAgent(t, [][]string{{"logs.slow_ms", "20"}, {"logs.buffer", "false"}})
	serve(a, "/slow", 500, 30*time.Millisecond, "", threeLines)
	if got := messages(queued(a)); got != "info line,warn line" {
		t.Fatalf("buffer off: %q", got)
	}
}

func TestLogOverrides(t *testing.T) {
	until := time.Now().Add(time.Hour).UTC().Format(tsLayout)
	past := time.Now().Add(-time.Hour).UTC().Format(tsLayout)
	ov, _ := json.Marshal([]map[string]string{
		{"route": "/checkout", "level": "debug", "until": until},
		{"user": "42", "level": "trace"},
		{"route": "/health", "level": "warn"},
		{"route": "/old", "level": "debug", "until": past},
		{"service": "other", "level": "debug"},
	})
	a := logAgent(t, [][]string{{"logs.overrides", string(ov)}})

	serve(a, "/checkout", 200, 0, "", threeLines)
	if got := messages(queued(a)); got != "debug line,info line,warn line" {
		t.Fatalf("route override: %q", got)
	}
	serve(a, "/health", 200, 0, "", threeLines)
	if got := messages(queued(a)); got != "warn line" {
		t.Fatalf("quieter route: %q", got)
	}
	serve(a, "/old", 200, 0, "", threeLines)
	if got := messages(queued(a)); got != "info line,warn line" {
		t.Fatalf("expired override: %q", got)
	}
	serve(a, "/any", 200, 0, "42", func(ctx context.Context, l *slog.Logger) {
		l.Log(ctx, slog.LevelDebug-4, "trace line")
		time.Sleep(2 * time.Millisecond)
		threeLines(ctx, l)
	})
	got := queued(a)
	if messages(got) != "trace line,debug line,info line,warn line" {
		t.Fatalf("user override: %q", messages(got))
	}
	if got[0].userID != "42" {
		t.Fatalf("user on held line: %+v", got[0])
	}
}

func TestLogsOutsideRequests(t *testing.T) {
	a := logAgent(t, [][]string{{"logs.level", "warn"}})
	active.Store(a)
	defer active.Store(nil)
	l := slog.New(LogHandler(nil))
	l.Info("startup")
	l.Error("boom", "err", "disk full", "request_id", "abc", "nested", slog.GroupValue(slog.Int("n", 3)))
	got := queued(a)
	if len(got) != 1 || got[0].message != "boom" || got[0].requestID != "abc" {
		t.Fatalf("got %q", messages(got))
	}
	if got[0].attrs["err"] != "disk full" || got[0].attrs["nested.n"] != int64(3) {
		t.Fatalf("attrs %+v", got[0].attrs)
	}
	if !strings.HasSuffix(got[0].file, "logs_test.go") || got[0].line == 0 || !strings.Contains(got[0].fn, "TestLogsOutsideRequests") {
		t.Fatalf("code location %q:%d %q", got[0].file, got[0].line, got[0].fn)
	}
}

func TestLogDropsAndRate(t *testing.T) {
	drops, _ := json.Marshal([]map[string]string{{"contains": "healthcheck"}, {"logger": "noisy", "level": "info"}})
	a := logAgent(t, [][]string{{"logs.drop", string(drops)}, {"logs.rate", "3"}})
	active.Store(a)
	defer active.Store(nil)
	l := slog.New(LogHandler(nil))
	l.Info("healthcheck ok")
	l.Info("chatty", "logger", "noisy")
	l.Warn("kept", "logger", "noisy")
	for range 5 {
		l.Info("flood")
	}
	l.Error("still kept")
	got := messages(queued(a))
	if got != "kept,flood,flood,still kept" {
		t.Fatalf("got %q", got)
	}
}

func TestLogsSkipTheSDKsOwnMessages(t *testing.T) {
	a := logAgent(t, nil)
	active.Store(a)
	defer active.Store(nil)
	slog.New(LogHandler(nil)).Error("visionary: write failed", "err", "db down")
	if got := queued(a); len(got) != 0 {
		t.Fatalf("got %q", messages(got))
	}
}

func TestLogHandlerForwards(t *testing.T) {
	var out strings.Builder
	next := slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelWarn})
	a := logAgent(t, [][]string{{"logs.level", "debug"}})
	active.Store(a)
	defer active.Store(nil)
	l := slog.New(LogHandler(next)).With("svc", "x").WithGroup("g")
	l.Debug("quiet", "k", 1)
	l.Warn("loud", "k", 2)
	if strings.Contains(out.String(), "quiet") || !strings.Contains(out.String(), "g.k=2") {
		t.Fatalf("next got %q", out.String())
	}
	got := queued(a)
	if messages(got) != "quiet,loud" || got[1].attrs["svc"] != "x" || got[1].attrs["g.k"] != int64(2) {
		t.Fatalf("captured %q %+v", messages(got), got)
	}
}

func TestCaptureStdLog(t *testing.T) {
	a := logAgent(t, nil)
	prevFlags := log.Flags()
	defer log.SetFlags(prevFlags)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	restore := a.CaptureStdLog()
	defer restore()
	log.Printf("platform: listening on :8080")
	log.Printf("ERROR payment failed for order %d", 7)
	log.Print("[warn] slow\nsecond line")
	log.Print("INFO visionary: profiling settings unavailable")
	log.Print("WARNING")
	got := queued(a)
	if messages(got) != "platform: listening on :8080,payment failed for order 7,[warn] slow\nsecond line,WARNING" {
		t.Fatalf("got %q", messages(got))
	}
	if got[0].sev != sevInfo || got[1].sev != sevError || got[2].sev != sevWarn {
		t.Fatalf("levels %d %d %d", got[0].sev, got[1].sev, got[2].sev)
	}
	// Here the SDK's own package logs, so the first frame outside it is the
	// test runner; in an application it is the line that called log.
	if got[0].file == "" || got[0].line == 0 {
		t.Fatalf("no code location: %+v", got[0])
	}
}

func TestLogParamsMatchColumns(t *testing.T) {
	a := logAgent(t, nil)
	p := a.logs.w.logParams(&logRecord{at: time.Now(), sev: sevWarn, message: "hi", attrs: map[string]any{"a": 1}, line: 3})
	if len(p) != len(logColumns) {
		t.Fatalf("%d params for %d columns", len(p), len(logColumns))
	}
	if p[7] != "warn" || p[8] != sevWarn || p[10] != "hi" || p[18] != 3 {
		t.Fatalf("params %v", p)
	}
}

func TestTraceParentID(t *testing.T) {
	if got := traceParentID("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatal(got)
	}
	for _, bad := range []string{"", "00-xyz-1-1", "00-00000000000000000000000000000000-00f067aa0ba902b7-01"} {
		if traceParentID(bad) != "" {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestStripStdHeader(t *testing.T) {
	if got := stripStdHeader("2026/10/28 09:30:12.123456 main.go:42: hello: world", log.LstdFlags|log.Lmicroseconds|log.Lshortfile); got != "hello: world" {
		t.Fatal(got)
	}
	if got := stripStdHeader("plain", 0); got != "plain" {
		t.Fatal(got)
	}
}
