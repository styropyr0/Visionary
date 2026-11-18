package visionary

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// rlStore answers the two rate-limit settings reads and keeps what is written.
type rlStore struct {
	fakeStore
	mu      sync.Mutex
	version string
	config  string
	reads   []string
}

func (s *rlStore) rows(_ context.Context, q string, _ ...any) ([][]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, q)
	switch {
	case strings.Contains(q, "ratelimit.version"):
		if s.version == "" {
			return nil, nil
		}
		return [][]string{{s.version}}, nil
	case strings.Contains(q, "ratelimit.config"):
		return [][]string{{s.config}}, nil
	}
	return nil, nil
}

func (s *rlStore) batch(ctx context.Context, stmts []stmt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fakeStore.batch(ctx, stmts)
}

func rlAgent(t *testing.T, st *rlStore) *Agent {
	t.Helper()
	cfg := Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Service: "api"}
	a := &Agent{cfg: cfg, db: st, ident: identity{service: "api", instance: "i-1"}, rl: newLimiter(cfg)}
	a.rlStop, a.rlDone = make(chan struct{}), make(chan struct{})
	go a.rateLimitLoop(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for !a.rl.Active() {
		if time.Now().After(deadline) {
			t.Fatal("rules never loaded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return a
}

func TestMiddlewareEnforcesRateLimits(t *testing.T) {
	st := &rlStore{version: "v1", config: `{"rules":[
		{"id":"r1","name":"Export","action":"limit","methods":["POST"],"paths":["/export*"],"limit":1,"window_s":60,"algorithm":"fixed"},
		{"id":"r2","action":"limit","key":"user","limit":0,"window_s":60,"match":{"attr:plan":["free"]}}
	]}`}
	a := rlAgent(t, st)
	handled := 0
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetUser(r.Context(), User{ID: "u1"})
		if RateLimited(w, r, map[string]string{"plan": r.URL.Query().Get("plan")}) {
			return
		}
		handled++
		w.WriteHeader(204)
	}))
	do := func(method, target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
		return rec
	}
	if rec := do("POST", "/export/1"); rec.Code != 204 {
		t.Fatal("first export", rec.Code)
	}
	rec := do("POST", "/export/2")
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"rate_limited"`) || handled != 1 {
		t.Fatalf("second export: %d %q %s handled=%d", rec.Code, rec.Header().Get("Retry-After"), rec.Body, handled)
	}
	if rec := do("GET", "/other?plan=pro"); rec.Code != 204 {
		t.Fatal("pro user", rec.Code)
	}
	if rec := do("GET", "/other?plan=free"); rec.Code != 429 || handled != 2 {
		t.Fatal("free user", rec.Code, handled)
	}

	close(a.rlStop)
	<-a.rlDone
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.written) != 1 || !strings.Contains(st.written[0].sql, "INSERT INTO rate_limit_events") || st.written[0].rows != 2 {
		t.Fatalf("written %+v", st.written)
	}
	args := st.written[0].args
	if args[1] != "api" || args[2] != "i-1" || args[3] != "r1" || args[5] != "limited" || args[12] != int64(1) {
		t.Fatalf("row %v", args[:13])
	}
}

func TestRateLimitPollReadsConfigOnlyOnNewVersion(t *testing.T) {
	st := &rlStore{version: "v1", config: `{"rules":[{"id":"b","action":"block"}]}`}
	a := rlAgent(t, st)
	defer func() { close(a.rlStop); <-a.rlDone }()
	st.mu.Lock()
	reads := strings.Join(st.reads, "\n")
	st.mu.Unlock()
	if strings.Count(reads, "ratelimit.config") != 1 {
		t.Fatal(reads)
	}
	if RateLimited(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), nil) {
		t.Fatal("RateLimited outside Middleware answered")
	}
}

func TestRateLimitOffLeavesMiddlewareAlone(t *testing.T) {
	a := &Agent{cfg: Config{RateLimitOff: true}}
	a.rl = newLimiter(a.cfg)
	if a.rl != nil {
		t.Fatal("limiter made with VISIONARY_RATE_LIMIT=false")
	}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if h := a.Middleware(next); h == nil {
		t.Fatal("nil handler")
	}
}
