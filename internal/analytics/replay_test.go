package analytics

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func replayPost(s *Server, q, body string, gz bool) *httptest.ResponseRecorder {
	b := []byte(body)
	if gz {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(b)
		_ = zw.Close()
		b = buf.Bytes()
	}
	r := httptest.NewRequest("POST", "/api/ingest/replay?"+q, bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer pk1")
	w := httptest.NewRecorder()
	s.handleReplayIngest(w, r)
	return w
}

func TestReplayIngestAndRead(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	l := s.live()
	l.Config["client_keys"] = "pk1"
	now := time.Now().UnixMilli()
	ms := strconv.FormatInt(now, 10)
	events := `[[` + ms + `,1,{"node":{"i":1,"t":1,"n":"html"}}],[` + ms + `,3,[1,2]]]`
	q := "sid=sess-r1&seq=0&start=" + ms + "&end=" + ms + "&enc=gzip&app=web&user=u5&url=https://shop.example/cart?token=x"

	if w := replayPost(s, q, events, true); w.Code != 403 {
		t.Fatalf("replay off: %d %s", w.Code, w.Body)
	}
	l.Config["replay"] = "true"
	if w := replayPost(s, q, events, true); w.Code != 204 {
		t.Fatalf("gzip chunk: %d %s", w.Code, w.Body)
	}
	if w := replayPost(s, strings.Replace(strings.Replace(q, "seq=0", "seq=1", 1), "enc=gzip", "enc=json", 1), events, false); w.Code != 204 {
		t.Fatalf("json chunk: %d %s", w.Code, w.Body)
	}
	if w := replayPost(s, strings.Replace(q, "seq=0", "seq=2", 1), `{"not":"an array"}`, true); w.Code != 400 {
		t.Fatalf("bad chunk: %d", w.Code)
	}
	if w := replayPost(s, "seq=3&start=1&end=1&enc=json", events, false); w.Code != 400 {
		t.Fatalf("no sid: %d", w.Code)
	}

	res, err := s.db.Query(ctx, `SELECT chunks, user_id, url, reason FROM replay_sessions WHERE session_id = 'sess-r1'`)
	if err != nil || len(res.Rows) != 1 {
		t.Fatal(err, res)
	}
	if row := res.Rows[0]; toInt(row[0]) != 2 || toString(row[1]) != "u5" || toString(row[2]) != "https://shop.example/cart" || toString(row[3]) != "sample" {
		t.Fatalf("session row %v", row)
	}

	r := httptest.NewRequest("GET", "/api/rum/replay/sess-r1", nil)
	r.SetPathValue("id", "sess-r1")
	w := httptest.NewRecorder()
	s.handleReplay(w, r)
	var meta struct {
		Chunks []struct{ Seq int64 } `json:"chunks"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &meta)
	if w.Code != 200 || len(meta.Chunks) != 2 || meta.Chunks[1].Seq != 1 {
		t.Fatalf("meta %d %s", w.Code, w.Body)
	}
	for _, seq := range []string{"0", "1"} {
		r = httptest.NewRequest("GET", "/api/rum/replay/sess-r1/chunk/"+seq, nil)
		r.SetPathValue("id", "sess-r1")
		r.SetPathValue("seq", seq)
		w = httptest.NewRecorder()
		s.handleReplayChunk(w, r)
		zr, err := gzip.NewReader(w.Body)
		if err != nil || w.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("chunk %s: %v %v", seq, err, w.Header())
		}
		got, _ := io.ReadAll(zr)
		if string(got) != events {
			t.Fatalf("chunk %s: %s", seq, got)
		}
	}

	q2 := "sid=sess-r2&seq=0&start=" + ms + "&end=" + strconv.FormatInt(now+1000, 10) + "&enc=json"
	if w := replayPost(s, q2, events, false); w.Code != 204 {
		t.Fatalf("second session: %d", w.Code)
	}
	if _, err := s.trimReplays(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got := s.replaySessions(ctx, []any{"sess-r1", "sess-r2"}); got["sess-r1"] || !got["sess-r2"] {
		t.Fatalf("after trim %v", got)
	}
	if res, _ := s.db.Query(ctx, `SELECT COUNT(*) FROM replay_chunks WHERE session_id = 'sess-r1'`); toInt(res.Rows[0][0]) != 0 {
		t.Fatal("sess-r1 chunks left")
	}
}

func TestReplayFrameHeaders(t *testing.T) {
	s := testServer()
	w := httptest.NewRecorder()
	s.handleReplayFrame(w, httptest.NewRequest("GET", "/replay-frame.html", nil))
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'none'") || w.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatalf("headers %v", w.Header())
	}
}

func TestReplayArchivePaged(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	fake := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := archiveConf{endpoint: srv.URL, bucket: "logs", region: "auto", access: "AKID", secret: "secret"}
	old := time.Now().UTC().Add(-2 * replayArchiveIdle).Format(tsLayout)
	if err := s.db.Exec(ctx, `INSERT INTO replay_sessions (session_id, started_at, created_at) VALUES ('s1', ?, ?)`, old, old); err != nil {
		t.Fatal(err)
	}
	n := 2*replayArchivePage + 3
	for i := 0; i < n; i++ {
		if err := s.db.Exec(ctx, `INSERT INTO replay_chunks (session_id, seq, created_at, ended_at, events, bytes, payload) VALUES ('s1', ?, ?, ?, 1, 1, 'x')`, i, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if done, err := s.archiveReplays(ctx, c, time.Now()); err != nil || done != 1 {
		t.Fatalf("archived %d %v", done, err)
	}
	for _, b := range fake.objects {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(zr)
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if len(lines) != n || !strings.Contains(lines[n-1], `"seq":`+strconv.Itoa(n-1)) {
			t.Fatalf("%d lines, last %s", len(lines), lines[len(lines)-1])
		}
		return
	}
	t.Fatal("nothing written")
}
