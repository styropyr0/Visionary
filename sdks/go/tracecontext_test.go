package visionary

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	inTrace  = "4bf92f3577b34da6a3ce929d0e0e4736"
	inParent = "00f067aa0ba902b7"
)

func TestParseTraceparent(t *testing.T) {
	sc, ok := parseTraceparent("00-" + inTrace + "-" + inParent + "-01")
	if !ok || sc.traceID != inTrace || sc.spanID != inParent || sc.flags != "01" {
		t.Fatalf("%+v %v", sc, ok)
	}
	if _, ok := parseTraceparent("01-" + inTrace + "-" + inParent + "-00-extra"); !ok {
		t.Fatal("a later version may add fields")
	}
	for _, bad := range []string{
		"", "00-xyz-1-1",
		"00-00000000000000000000000000000000-" + inParent + "-01",
		"00-" + inTrace + "-0000000000000000-01",
		"ff-" + inTrace + "-" + inParent + "-01",
		"00-" + strings.ToUpper(inTrace) + "-" + inParent + "-01",
		"00-" + inTrace + "-" + inParent + "-01-extra",
	} {
		if _, ok := parseTraceparent(bad); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestNewIDs(t *testing.T) {
	a, b := newTraceID(), newSpanID()
	if len(a) != 32 || len(b) != 16 || !isLowerHex(a) || !isLowerHex(b) || a == newTraceID() {
		t.Fatal(a, b)
	}
}

func traceAgent() *Agent {
	a := &Agent{
		cfg:   Config{Logger: slog.Default(), Requests: RequestsConfig{Enabled: true}, Spans: true, SpanMax: 100, SpanThreshold: time.Hour},
		db:    &fakeStore{},
		ident: identity{service: "api", instance: "i-1"},
	}
	a.writer = newWriter(RequestsConfig{QueueSize: 1000}, true, a.ident, a.db, a.cfg.Logger)
	return a
}

func written(a *Agent) (entries []*entry, spans []*spanRecord) {
	for {
		select {
		case r := <-a.writer.queue:
			switch v := r.(type) {
			case *entry:
				entries = append(entries, v)
			case *spanRecord:
				spans = append(spans, v)
			}
		default:
			return
		}
	}
}

func TestRequestContinuesTrace(t *testing.T) {
	a := traceAgent()
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		StartSpan(r.Context(), KindCache, "GET session")(nil)
		ctx, end := StartSpanContext(r.Context(), KindCustom, "build report")
		StartSpan(ctx, KindDB, "SELECT orders")(nil)
		end(nil)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("traceparent", "00-"+inTrace+"-"+inParent+"-01")
	h.ServeHTTP(httptest.NewRecorder(), req)

	entries, _ := written(a)
	if len(entries) != 1 {
		t.Fatalf("%d entries", len(entries))
	}
	e := entries[0]
	if e.TraceID != inTrace || e.ParentSpanID != inParent || len(e.SpanID) != 16 || e.SpanID == inParent {
		t.Fatalf("request %q %q %q", e.TraceID, e.SpanID, e.ParentSpanID)
	}
	by := map[string]*spanRecord{}
	for _, s := range e.Spans {
		if s.traceID != inTrace || len(s.spanID) != 16 {
			t.Fatalf("span %+v", s)
		}
		by[s.name] = s
	}
	if by["GET session"].parentID != e.SpanID || by["build report"].parentID != e.SpanID {
		t.Fatal("direct calls are children of the request")
	}
	if by["SELECT orders"].parentID != by["build report"].spanID {
		t.Fatal("a call inside a custom span nests under it")
	}
	p := a.writer.requestParams(e, nil)
	if len(p) != len(requestColumns) || p[len(p)-3] != inTrace || p[len(p)-1] != inParent {
		t.Fatalf("request params %v", p[len(p)-3:])
	}
	sp := a.writer.spanParams(by["SELECT orders"])
	if len(sp) != len(spanColumns) || sp[len(sp)-3] != inTrace || sp[len(sp)-1] != by["build report"].spanID {
		t.Fatalf("span params %v", sp[len(sp)-3:])
	}
}

func TestRequestStartsTrace(t *testing.T) {
	a := traceAgent()
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("traceparent", "00-"+strings.Repeat("0", 32)+"-"+inParent+"-01")
	h.ServeHTTP(httptest.NewRecorder(), req)
	entries, _ := written(a)
	e := entries[0]
	if len(e.TraceID) != 32 || e.TraceID == strings.Repeat("0", 32) || e.ParentSpanID != "" || len(e.SpanID) != 16 {
		t.Fatalf("%q %q %q", e.TraceID, e.SpanID, e.ParentSpanID)
	}
}

func TestTransportPropagates(t *testing.T) {
	var got http.Header
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer down.Close()
	client := &http.Client{Transport: Transport(nil)}

	a := traceAgent()
	var outgoing *http.Request
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outgoing, _ = http.NewRequestWithContext(r.Context(), "GET", down.URL+"/y", nil)
		resp, err := client.Do(outgoing)
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("traceparent", "00-"+inTrace+"-"+inParent+"-01")
	req.Header.Set("tracestate", "vendor=abc")
	h.ServeHTTP(httptest.NewRecorder(), req)

	entries, _ := written(a)
	e := entries[0]
	if len(e.Spans) != 1 || e.Spans[0].kind != KindHTTP {
		t.Fatalf("spans %+v", e.Spans)
	}
	call := e.Spans[0]
	if want := "00-" + inTrace + "-" + call.spanID + "-01"; got.Get("traceparent") != want {
		t.Fatalf("traceparent %q, want %q", got.Get("traceparent"), want)
	}
	if call.parentID != e.SpanID || got.Get("tracestate") != "vendor=abc" {
		t.Fatalf("parent %q tracestate %q", call.parentID, got.Get("tracestate"))
	}
	if outgoing.Header.Get("traceparent") != "" {
		t.Fatal("the caller's request was changed")
	}

	own := "00-" + strings.Repeat("a", 32) + "-" + strings.Repeat("b", 16) + "-00"
	r2, _ := http.NewRequest("GET", down.URL, nil)
	r2.Header.Set("traceparent", own)
	resp, err := client.Do(r2)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got.Get("traceparent") != own {
		t.Fatalf("own traceparent replaced: %q", got.Get("traceparent"))
	}
}

func TestBackgroundKeepsTrace(t *testing.T) {
	a := traceAgent()
	done := make(chan struct{})
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Go(r.Context(), "send email", func(ctx context.Context) error {
			defer close(done)
			StartSpan(ctx, KindHTTP, "POST mail")(nil)
			return nil
		})
		<-done
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	deadline := time.Now().Add(2 * time.Second)
	var e *entry
	var late []*spanRecord
	for time.Now().Before(deadline) {
		es, ss := written(a)
		if len(es) > 0 {
			e = es[0]
		}
		late = append(late, ss...)
		if e != nil && len(e.Spans)+len(late) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	by := map[string]*spanRecord{}
	for _, s := range append(e.Spans, late...) {
		by[s.name] = s
	}
	bg, call := by["send email"], by["POST mail"]
	if bg == nil || call == nil {
		t.Fatalf("spans %v", by)
	}
	if bg.traceID != e.TraceID || bg.parentID != e.SpanID || call.traceID != e.TraceID || call.parentID != bg.spanID {
		t.Fatalf("request %s/%s, background %+v, call %+v", e.TraceID, e.SpanID, bg, call)
	}
}

func TestLogLinesCarrySpan(t *testing.T) {
	a := logAgent(t, nil)
	var reqSpan string
	serve(a, "/x", 200, 0, "", func(ctx context.Context, l *slog.Logger) {
		reqSpan = currentSpan(ctx).spanID
		l.InfoContext(ctx, "hello")
	})
	recs := queued(a)
	if len(recs) != 1 || len(recs[0].traceID) != 32 || recs[0].spanID != reqSpan || reqSpan == "" {
		t.Fatalf("%+v", recs)
	}
}
