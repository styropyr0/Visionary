package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/styropyr0/visionary/internal/db"
	visionary "github.com/styropyr0/visionary/sdks/go"
)

var benchTables = []string{"api_logs", "spans", "errors", "app_logs", "metrics"}

type writeResult struct {
	backend   string
	served    int64
	behind    int64
	elapsed   time.Duration
	drain     time.Duration
	rows      map[string]int64
	stats     visionary.WriterStats
	maxQueued int
	sizeFrom  int64
	sizeTo    int64
}

func (r writeResult) total() int64 {
	var n int64
	for _, t := range benchTables {
		n += r.rows[t]
	}
	return n
}

func (r writeResult) markdown(o options) string {
	var b strings.Builder
	secs := (r.elapsed + r.drain).Seconds()
	fmt.Fprintf(&b, "\nWrite: %s, %d req/s offered for %s, %d spans and %d log lines a request, %.1f%% errors.\n\n",
		r.backend, o.rate, o.duration, o.spans, o.logs, o.errPct)
	b.WriteString("| Requests/s served | Rows written | Rows/s | Dropped | Max SDK queue | Drain after stop | DB growth |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	fmt.Fprintf(&b, "| %.0f | %d | %.0f | %d | %d | %.1f s | %s |\n",
		float64(r.served)/r.elapsed.Seconds(), r.total(), float64(r.total())/secs, r.stats.Dropped, r.maxQueued,
		r.drain.Seconds(), bytesText(r.sizeTo-r.sizeFrom))
	var parts []string
	for _, t := range benchTables {
		parts = append(parts, fmt.Sprintf("%s %d", t, r.rows[t]))
	}
	fmt.Fprintf(&b, "\nRows by table: %s.", strings.Join(parts, ", "))
	if r.behind > 0 {
		fmt.Fprintf(&b, " The generator fell %d requests behind the target rate.", r.behind)
	}
	b.WriteString("\n")
	return b.String()
}

func runWrite(ctx context.Context, o options) (*writeResult, error) {
	conn, err := db.Open(db.Config{URL: o.db})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	res := &writeResult{backend: conn.Name(), rows: map[string]int64{}}
	if err := uncapLogs(ctx, conn); err != nil {
		return nil, fmt.Errorf("lift the log rate cap: %w", err)
	}
	before := countRows(ctx, conn)
	res.sizeFrom = dbSize(ctx, conn)

	agent, err := visionary.New(visionary.Config{
		DatabaseURL: o.db,
		Service:     "bench",
		Instance:    "bench-1",
		Release:     "bench",
		Spans:       true,
		SpanMax:     100,
		Errors:      true,
		ProfileOff:  true,
		ExplainOff:  true,
		Logger:      slog.Default(),
		Requests:    visionary.RequestsConfig{Enabled: true, CaptureBodies: o.bodies, MaxBodyBytes: 8 << 10, BatchSize: o.batch},
		System:      visionary.SystemConfig{Enabled: true, Interval: 15 * time.Second},
		Logs:        visionary.LogsConfig{Enabled: true, Level: "info"},
		Metrics:     visionary.MetricsConfig{Enabled: true, Interval: 5 * time.Second, MaxSeries: 1000},
	})
	if err != nil {
		return nil, err
	}
	agentCtx, stopAgent := context.WithCancel(ctx)
	defer stopAgent()
	agent.Start(agentCtx)
	time.Sleep(500 * time.Millisecond)

	handler := agent.Middleware(benchMux(o))
	jobs := make(chan int, o.workers*4)
	var served atomic.Int64
	var wg sync.WaitGroup
	for range o.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				serveOne(handler, n, o)
				served.Add(1)
			}
		}()
	}

	var maxQueued atomic.Int64
	sampleDone := make(chan struct{})
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-sampleDone:
				return
			case <-t.C:
				if q := int64(agent.WriterStats().Queued); q > maxQueued.Load() {
					maxQueued.Store(q)
				}
			}
		}
	}()

	start := time.Now()
	const tick = 5 * time.Millisecond
	ticker := time.NewTicker(tick)
	var sent, behind int64
	for now := range ticker.C {
		elapsed := now.Sub(start)
		if elapsed >= o.duration {
			break
		}
		want := int64(elapsed.Seconds() * float64(o.rate))
		for ; sent < want; sent++ {
			select {
			case jobs <- int(sent):
			default:
				behind += want - sent
				sent = want
			}
		}
	}
	ticker.Stop()
	close(jobs)
	wg.Wait()
	res.elapsed = time.Since(start)
	res.served, res.behind = served.Load(), behind

	drainStart := time.Now()
	closeCtx, cancel := context.WithTimeout(ctx, o.drain)
	agent.Close(closeCtx)
	cancel()
	res.drain = time.Since(drainStart)
	close(sampleDone)
	res.stats = agent.WriterStats()
	res.maxQueued = int(maxQueued.Load())

	after := countRows(ctx, conn)
	for _, t := range benchTables {
		res.rows[t] = after[t] - before[t]
	}
	res.sizeTo = dbSize(ctx, conn)
	return res, nil
}

var (
	routes    = []string{"GET /api/items/{id}", "POST /api/orders", "GET /api/search", "GET /api/users/{id}/cart", "PUT /api/cart/{id}"}
	spanNames = []string{"SELECT items", "SELECT users", "INSERT orders", "UPDATE stock", "GET payments.example/charge", "GET cache session"}
	logLines  = []string{"loaded items from catalog", "charge accepted by provider", "cart updated", "cache miss for session", "search took longer than expected"}
	body      = `{"items":[{"id":1,"name":"Blue mug","price":12.5,"qty":2},{"id":2,"name":"Tea, loose leaf","price":8,"qty":1}],"total":33,"currency":"EUR","note":"leave at the door"}`
)

// benchMux answers like a small shop API: each request makes a few calls,
// logs a few lines and counts itself.
func benchMux(o options) http.Handler {
	logger := slog.New(visionary.LogHandler(slog.DiscardHandler))
	mux := http.NewServeMux()
	for _, pattern := range routes {
		route := pattern[strings.Index(pattern, " ")+1:]
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			started := time.Now()
			if uid := r.Header.Get("X-Bench-User"); uid != "" {
				visionary.SetUser(ctx, visionary.User{ID: uid, Role: "customer"})
			}
			for i := range o.spans {
				end := visionary.StartSpan(ctx, kindOf(i), spanNames[(i+len(route))%len(spanNames)])
				end(nil)
			}
			for i := range o.logs {
				logger.InfoContext(ctx, logLines[(i+len(route))%len(logLines)], "route", route, "attempt", i+1)
			}
			visionary.Counter("bench_requests", "route", route).Add(1)
			failed := r.Header.Get("X-Bench-Fail") == "1"
			if failed {
				err := errors.New("payment provider timed out")
				visionary.CaptureError(ctx, err)
				logger.ErrorContext(ctx, "order failed", "err", err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":{"code":"upstream_timeout","key":"payment"}}`))
			} else {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}
			visionary.Histogram("bench_handler_ms", "route", route).Observe(float64(time.Since(started).Microseconds()) / 1000)
		})
	}
	return mux
}

func kindOf(i int) string {
	switch i % 3 {
	case 1:
		return visionary.KindHTTP
	case 2:
		return visionary.KindCache
	}
	return visionary.KindDB
}

func serveOne(h http.Handler, n int, o options) {
	pattern := routes[n%len(routes)]
	method, path, _ := strings.Cut(pattern, " ")
	path = strings.NewReplacer("{id}", strconv.Itoa(n%5000)).Replace(path)
	var r *http.Request
	if method == http.MethodGet {
		r = httptest.NewRequest(method, path+"?page=1", nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.RemoteAddr = fmt.Sprintf("10.0.%d.%d:40000", n%250, n%200)
	r.Header.Set("User-Agent", "visionary-bench/1.0")
	r.Header.Set("X-Bench-User", strconv.Itoa(n%500))
	if n%2 == 0 {
		r.Header.Set("traceparent", "00-"+randomHex(16)+"-"+randomHex(8)+"-01")
	}
	if o.errPct > 0 && mrand.Float64()*100 < o.errPct {
		r.Header.Set("X-Bench-Fail", "1")
	}
	h.ServeHTTP(httptest.NewRecorder(), r)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// uncapLogs lifts Settings → Logs' per-minute cap, so the log writer sees
// the full load. The table is created as the server would.
func uncapLogs(ctx context.Context, conn db.DB) error {
	name, ts := "TEXT", "TEXT"
	if conn.Dialect() == visionary.MySQL {
		name, ts = "VARCHAR(191)", "VARCHAR(32)"
	}
	if err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS visionary_settings (name `+name+` NOT NULL PRIMARY KEY, value TEXT NOT NULL, updated_at `+ts+` NOT NULL)`); err != nil {
		return err
	}
	if err := conn.Exec(ctx, `DELETE FROM visionary_settings WHERE name = 'logs.rate'`); err != nil {
		return err
	}
	return conn.Exec(ctx, `INSERT INTO visionary_settings (name, value, updated_at) VALUES ('logs.rate', '0', ?)`,
		time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
}

func countRows(ctx context.Context, conn db.DB) map[string]int64 {
	out := map[string]int64{}
	for _, t := range benchTables {
		if res, err := conn.Query(ctx, `SELECT COUNT(*) FROM `+t); err == nil && len(res.Rows) > 0 {
			out[t] = toInt(res.Rows[0][0])
		}
	}
	return out
}

// dbSize is the database's size on disk in bytes, or 0 when unknown.
func dbSize(ctx context.Context, conn db.DB) int64 {
	q := `SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`
	switch conn.Dialect() {
	case visionary.Postgres:
		q = `SELECT pg_database_size(current_database())`
	case visionary.MySQL:
		q = `SELECT SUM(data_length + index_length) FROM information_schema.tables WHERE table_schema = DATABASE()`
	}
	res, err := conn.Query(ctx, q)
	if err != nil || len(res.Rows) == 0 {
		return 0
	}
	return toInt(res.Rows[0][0])
}

func toInt(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	case []byte:
		n, _ := strconv.ParseInt(string(t), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	n, _ := strconv.ParseInt(fmt.Sprint(v), 10, 64)
	return n
}

func bytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n/1024)
}
