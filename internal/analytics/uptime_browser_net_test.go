package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/styropyr0/visionary/internal/uptimecore"
)

func TestBrowserRequestsAndStepShots(t *testing.T) {
	s, u := browserRunner(t)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<!doctype html><h1>Shop</h1><img src="/logo.png"><script src="/app.js?token=abc123&v=2"></script>`)
		case "/logo.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(make([]byte, 2048))
		case "/app.js":
			time.Sleep(50 * time.Millisecond)
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = io.WriteString(w, `fetch("/api/cart?key=hunter22-secret").catch(() => {});`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()
	c := UptimeCheck{Name: "Shop", Kind: "browser", Enabled: true, Secrets: map[string]string{"KEY": "hunter22-secret"},
		Steps:          []BrowserStep{{Action: "goto", Value: site.URL + "/"}, {Action: "wait_text", Text: "Shop"}, {Action: "wait", Ms: 200}},
		BrowserOptions: BrowserOptions{StepScreenshots: true}}
	res, run := playCheck(t, u, c, false)
	if len(run.StepShots) != 3 || run.StepShots[0] == nil || len(run.StepShots[2]) > uptimecore.MaxStepShot {
		t.Fatalf("step shots: %d", len(run.StepShots))
	}
	byPath := map[string]netRequest{}
	for _, r := range run.Requests {
		byPath[strings.TrimPrefix(strings.SplitN(r.URL, "?", 2)[0], site.URL)] = r
		if strings.Contains(r.URL, "abc123") || strings.Contains(r.URL, "hunter22") {
			t.Errorf("secret in %s", r.URL)
		}
	}
	page, logo, js, api := byPath["/"], byPath["/logo.png"], byPath["/app.js"], byPath["/api/cart"]
	if page.Status != 200 || page.Type != "Document" || page.Method != "GET" || logo.Size < 2048 || logo.MIME != "image/png" {
		t.Fatalf("requests: %+v", run.Requests)
	}
	if js.DurationMs < 40 || js.StartMs < page.StartMs || api.Status != 404 || api.Type != "Fetch" {
		t.Fatalf("timing: page %+v js %+v api %+v", page, js, api)
	}

	if err := s.saveUptimeCheck(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	c.Steps[0].Value = site.URL + "/"
	if err := u.record(context.Background(), c, res); err != nil {
		t.Fatal(err)
	}
	if err := u.recordBrowser(context.Background(), c, res, run); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/uptime/checks/{id}/runs/{run}", s.handleBrowserRun)
	rows, err := s.db.Query(context.Background(), `SELECT id FROM uptime_browser_results WHERE check_id = ?`, c.ID)
	if err != nil || len(rows.Rows) != 1 {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf("/api/uptime/checks/%d/runs/%d", c.ID, toInt(rows.Rows[0][0])), nil))
	var detail struct {
		Requests  []netRequest `json:"requests"`
		StepShots []string     `json:"step_shots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil || len(detail.Requests) != len(run.Requests) || len(detail.StepShots) != 3 ||
		!strings.HasPrefix(detail.StepShots[1], "data:image/jpeg;base64,") {
		t.Fatalf("detail: %v %d %.200s", err, rec.Code, rec.Body)
	}
}

func TestBrowserPruneRunDetail(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	for i := range 30 {
		ok := 1
		if i%10 == 0 {
			ok = 0
		}
		if err := s.db.Exec(ctx, `INSERT INTO uptime_browser_results (created_at, check_id, ok, failed_step, steps, console, network, requests, step_shots)
VALUES (?, 7, ?, -1, '[]', '[]', '[]', '[]', '["x"]')`, "2026-10-01T00:00:"+fmt.Sprintf("%02d", i)+"Z", ok); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.pruneRunDetail(ctx, 7); err != nil {
		t.Fatal(err)
	}
	count := func(where string) int {
		rows, err := s.db.Query(ctx, `SELECT COUNT(*) FROM uptime_browser_results WHERE check_id = 7 AND `+where)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := strconv.Atoi(toString(rows.Rows[0][0]))
		return n
	}
	if n := count("step_shots IS NOT NULL"); n != browserKeepStepRuns {
		t.Errorf("step shots kept: %d", n)
	}
	if n := count("requests IS NOT NULL"); n != 30 {
		t.Errorf("requests kept: %d", n)
	}
}
