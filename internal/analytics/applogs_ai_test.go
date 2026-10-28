package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fakeModel(t *testing.T, s *Server, replies ...string) *[]string {
	t.Helper()
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		prompts = append(prompts, string(b))
		content := replies[min(len(prompts)-1, len(replies)-1)]
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	t.Cleanup(srv.Close)
	s.llmP.Store(&llm{apiKey: "k", apiBase: srv.URL, model: "m", http: srv.Client()})
	return &prompts
}

func TestLogAsk(t *testing.T) {
	s := logsTestServer(t, nil)
	seedLogs(t, s)
	prompts := fakeModel(t, s, `{"query":"level:loud","since":"1h"}`, `{"query":"level:error+ service:api","since":"1h","note":"API errors"}`)
	r := httptest.NewRequest("POST", "/api/applogs/ask", strings.NewReader(`{"question":"errors in the api in the last hour"}`))
	r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Role: "viewer"}))
	w := httptest.NewRecorder()
	s.handleLogAsk(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"query":"level:error+ service:api"`) || !strings.Contains(w.Body.String(), `"since":"1h"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(*prompts) != 2 || !strings.Contains((*prompts)[0], "Services: api") || !strings.Contains((*prompts)[1], "That search is invalid") {
		t.Fatalf("prompts %d", len(*prompts))
	}
}

func TestLogExplain(t *testing.T) {
	s := logsTestServer(t, nil)
	base := seedLogs(t, s)
	if _, err := s.patternPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	prompts := fakeModel(t, s, `{"summary":"Payments time out (#2).","lines":[2]}`)
	body, _ := json.Marshal(map[string]string{"q": "service:api", "from": base.Add(-time.Hour).Format(time.RFC3339), "to": base.Add(time.Hour).Format(time.RFC3339)})
	r := httptest.NewRequest("POST", "/api/applogs/explain", strings.NewReader(string(body)))
	r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Role: "admin"}))
	w := httptest.NewRecorder()
	s.handleLogExplain(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Payments time out") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var sent struct {
		Messages []chatMessage `json:"messages"`
	}
	_ = json.Unmarshal([]byte((*prompts)[0]), &sent)
	p := sent.Messages[1].Content
	if !strings.Contains(p, `payment timeout after <*>`) || !strings.Contains(p, "request r2") || strings.Contains(p, "hidden from viewers") {
		t.Fatalf("prompt %s", p)
	}
	s.llmP.Store(nil)
	w = httptest.NewRecorder()
	s.handleLogExplain(w, httptest.NewRequest("POST", "/api/applogs/explain", strings.NewReader(string(body))))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "analyst is off") {
		t.Fatalf("off: %d %s", w.Code, w.Body.String())
	}
}
