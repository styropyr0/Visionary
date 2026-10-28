package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

func TestDeleteLogsInRange(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	old, recent := now.Add(-3*time.Hour), now.Add(-10*time.Minute)
	lines := []*logLine{
		{at: old, service: "api", sev: logrules.Info, message: "only in the old hour"},
		{at: old, service: "api", sev: logrules.Info, message: "in both hours"},
		{at: recent, service: "api", sev: logrules.Info, message: "in both hours"},
		{at: recent, service: "api", sev: logrules.Info, message: "only recently"},
	}
	if _, err := s.ingestLogLines(ctx, lines); err != nil {
		t.Fatal(err)
	}
	if _, err := s.patternPass(ctx); err != nil {
		t.Fatal(err)
	}
	body := `{"kinds":["logs"],"from":"` + old.Add(-time.Minute).Format(time.RFC3339) + `","to":"` + old.Add(time.Minute).Format(time.RFC3339) + `"}`
	call := func(h http.HandlerFunc) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/data/x", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Name: "Ad", Role: "admin"}))
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	// Two old lines, and the pattern seen only then; the pattern that goes on
	// after the range stays.
	w := call(s.handleDataPreview)
	var out struct {
		Counts []dataCount `json:"counts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != 200 || err != nil || len(out.Counts) != 1 || out.Counts[0].Rows != 3 {
		t.Fatalf("preview = %d %s", w.Code, w.Body)
	}
	if w := call(s.handleDataDelete); w.Code != 200 {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	res, err := s.db.Query(ctx, `SELECT message FROM app_logs ORDER BY message`)
	if err != nil || len(res.Rows) != 2 || toString(res.Rows[0][0]) != "in both hours" || toString(res.Rows[1][0]) != "only recently" {
		t.Fatalf("lines left = %v %v", res.Rows, err)
	}
	res, err = s.db.Query(ctx, `SELECT template FROM log_patterns ORDER BY template`)
	if err != nil || len(res.Rows) != 2 {
		t.Fatalf("patterns left = %v %v", res.Rows, err)
	}
	for _, row := range res.Rows {
		if strings.Contains(toString(row[0]), "old") {
			t.Fatalf("pattern seen only in the range was kept: %v", res.Rows)
		}
	}
}

func TestHideLogsPage(t *testing.T) {
	s := logsTestServer(t, nil)
	r := httptest.NewRequest("PUT", "/api/settings/pages", strings.NewReader(`{"hidden_pages":["applogs","logs"]}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Name: "Ad", Role: "admin"}))
	w := httptest.NewRecorder()
	s.handlePageSettings(w, r)
	if w.Code != 200 {
		t.Fatalf("hide = %d %s", w.Code, w.Body)
	}
}
