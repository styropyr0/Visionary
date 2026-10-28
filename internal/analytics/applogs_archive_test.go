package analytics

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKID/") || !strings.Contains(auth, "/auto/s3/aws4_request") || r.Header.Get("x-amz-content-sha256") != "UNSIGNED-PAYLOAD" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.objects[r.URL.Path] = b
	case http.MethodGet:
		b, ok := f.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	}
}

func TestArchiveAndRehydrate(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	fake := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	l := s.live()
	for k, v := range map[string]string{
		"logs_archive_endpoint": srv.URL, "logs_archive_bucket": "logs", "logs_archive_access_key": "AKID",
		"logs_archive_secret_key": "secret", "logs_archive_prefix": "v/logs",
	} {
		l.Config[k] = v
	}
	now := time.Now().UTC()
	h1 := now.Truncate(time.Hour).Add(-3 * time.Hour)
	h2 := h1.Add(time.Hour)
	if _, err := s.ingestLogLines(ctx, []*logLine{
		{at: h1.Add(5 * time.Minute), service: "api", sev: logrules.Error, message: "first hour", attrs: map[string]any{"n": 1}, requestID: "r1", line: 9, file: "a.go"},
		{at: h1.Add(50 * time.Minute), service: "api", sev: logrules.Info, message: "first hour again"},
		{at: h2.Add(time.Minute), service: "web", sev: logrules.Warn, message: "second hour"},
		{at: now, service: "web", sev: logrules.Info, message: "this hour is not over"},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.archivePass(ctx, now)
	if err != nil || n != 3 {
		// h1, h2 and the empty hour after h2; not the current hour
		t.Fatalf("archived %d %v", n, err)
	}
	if n, _ := s.archivePass(ctx, now); n != 0 {
		t.Fatalf("archived again: %d", n)
	}
	key := "/logs/v/logs/" + h1.Format("2006/01/02/15") + ".ndjson.gz"
	if len(fake.objects[key]) == 0 {
		t.Fatalf("no object %s in %v", key, keys(fake.objects))
	}
	res, _ := s.db.Query(ctx, `SELECT hour, lines FROM log_archives ORDER BY hour`)
	if len(res.Rows) != 3 || toInt(res.Rows[0][1]) != 2 || toInt(res.Rows[1][1]) != 1 {
		t.Fatalf("archives %+v", res.Rows)
	}

	if err := s.db.Exec(ctx, `DELETE FROM app_logs WHERE created_at < ?`, h2.Format(tsLayout)); err != nil {
		t.Fatal(err)
	}
	body := `{"from":"` + h1.Format(time.RFC3339) + `","to":"` + h2.Add(time.Hour).Format(time.RFC3339) + `"}`
	call := func(role string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/applogs/archives/rehydrate", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Role: role}))
		w := httptest.NewRecorder()
		s.handleRehydrate(w, r)
		return w
	}
	if w := call("editor"); w.Code != 403 {
		t.Fatalf("editor rehydrated: %d", w.Code)
	}
	w := call("admin")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"lines":2`) || !strings.Contains(w.Body.String(), "still stored") {
		t.Fatalf("rehydrate %d %s", w.Code, w.Body.String())
	}
	rows := logRows(t, s)
	var back map[string]any
	for _, r := range rows {
		if r["message"] == "first hour" {
			back = r
		}
	}
	if back == nil || back["request_id"] != "r1" || back["level"] != "error" || toInt(back["code_line"]) != 9 || attrsOf(t, back)["n"] != float64(1) {
		t.Fatalf("rehydrated row %+v", back)
	}
	if w := call("admin"); !strings.Contains(w.Body.String(), `"lines":0`) {
		t.Fatalf("loaded twice: %s", w.Body.String())
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestArchiveSignature checks against a signature worked out by hand from the SigV4 steps.
func TestArchiveSignature(t *testing.T) {
	c := archiveConf{endpoint: "https://s3.example.com", bucket: "b", region: "us-east-1", access: "AKIDEXAMPLE", secret: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	req, _ := http.NewRequest(http.MethodGet, c.objectURL("logs/2026/10/28/11.ndjson.gz"), bytes.NewReader(nil))
	c.sign(req, time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261028/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=") ||
		req.URL.Path != "/b/logs/2026/10/28/11.ndjson.gz" || req.Header.Get("x-amz-date") != "20261028T120000Z" {
		t.Fatalf("%s %s", auth, req.URL.Path)
	}
	if sig := auth[strings.LastIndex(auth, "=")+1:]; len(sig) != 64 {
		t.Fatalf("signature %q", sig)
	}
}

// TestArchiveRealS3 runs against a real store when VISIONARY_TEST_S3=endpoint,bucket,access,secret is set.
func TestArchiveRealS3(t *testing.T) {
	conf := strings.Split(os.Getenv("VISIONARY_TEST_S3"), ",")
	if len(conf) != 4 {
		t.Skip("VISIONARY_TEST_S3 not set")
	}
	s := logsTestServer(t, nil)
	ctx := context.Background()
	l := s.live()
	l.Config["logs_archive_endpoint"], l.Config["logs_archive_bucket"] = conf[0], conf[1]
	l.Config["logs_archive_access_key"], l.Config["logs_archive_secret_key"] = conf[2], conf[3]
	l.Config["logs_archive_region"], l.Config["logs_archive_prefix"] = "us-east-1", "it/"+strconv.FormatInt(time.Now().UnixNano(), 36)
	h := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	if _, err := s.ingestLogLines(ctx, []*logLine{{at: h.Add(time.Minute), service: "api", sev: logrules.Warn, message: "to the real bucket"}}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.archivePass(ctx, time.Now()); err != nil || n == 0 {
		t.Fatalf("archive: %d %v", n, err)
	}
	c, _ := s.archiveConf()
	got, err := s.rehydrateObject(ctx, c, c.objectKey(h))
	if err != nil || got != 1 {
		t.Fatalf("read back: %d %v", got, err)
	}
}
