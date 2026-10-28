package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testShipper(t *testing.T, url string) *logShipper {
	t.Helper()
	cfg := config{url: url, key: "k1", host: "web-1", stateDir: t.TempDir()}
	return newLogShipper(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

func drain(s *logShipper) []*logEntry {
	var out []*logEntry
	for {
		select {
		case e := <-s.queue:
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestJournalEntry(t *testing.T) {
	line := `{"__CURSOR":"s=abc;i=1","__REALTIME_TIMESTAMP":"1792141200123456","PRIORITY":"3","_SYSTEMD_UNIT":"nginx.service","SYSLOG_IDENTIFIER":"nginx","_PID":"812","_HOSTNAME":"web 1","MESSAGE":"upstream timed out\n"}`
	e, cur, ok := journalEntry([]byte(line))
	if !ok || cur != "s=abc;i=1" || e.Message != "upstream timed out" || e.Level != "error" || e.Service != "nginx" || e.Unit != "nginx.service" || e.PID != "812" || e.Host != "web-1" {
		t.Fatalf("%+v %q", e, cur)
	}
	if e.Time != "2026-10-16T09:00:00.123456Z" {
		t.Fatalf("time %q", e.Time)
	}
	bin := `{"__CURSOR":"c2","MESSAGE":[104,105],"_SYSTEMD_UNIT":"cron.service"}`
	e, _, ok = journalEntry([]byte(bin))
	if !ok || e.Message != "hi" || e.Service != "cron" {
		t.Fatalf("binary %+v", e)
	}
	if _, cur, ok := journalEntry([]byte(`{"__CURSOR":"c3"}`)); ok || cur != "c3" {
		t.Fatal("empty message kept")
	}
}

func frame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

func TestDemuxDocker(t *testing.T) {
	var b bytes.Buffer
	b.Write(frame(1, "2026-10-28T10:00:00.000000001Z started\n2026-10-28T10:00:01Z hal"))
	b.Write(frame(2, "2026-10-28T10:00:02Z boom\n"))
	b.Write(frame(1, "f line\r\n"))
	b.Write(frame(1, "no newline at the end"))
	var got []string
	if err := demuxDocker(&b, func(stream, line string) { got = append(got, stream+"|"+line) }); err != nil {
		t.Fatal(err)
	}
	want := "stdout|2026-10-28T10:00:00.000000001Z started,stderr|2026-10-28T10:00:02Z boom,stdout|2026-10-28T10:00:01Z half line,stdout|no newline at the end"
	if strings.Join(got, ",") != want {
		t.Fatalf("got %q", got)
	}
	at, text := splitDockerTime("2026-10-28T10:00:02Z boom now")
	if at != "2026-10-28T10:00:02Z" || text != "boom now" {
		t.Fatal(at, text)
	}
	if at, text := splitDockerTime("plain"); at != "" || text != "plain" {
		t.Fatal(at, text)
	}
}

func TestReadFileFollowsRotation(t *testing.T) {
	s := testShipper(t, "http://x")
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx", "access.log")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.readFile(path)
	if got := drain(s); len(got) != 0 {
		t.Fatalf("read history: %+v", got)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("GET / 200\npartial")
	f.Close()
	s.readFile(path)
	got := drain(s)
	if len(got) != 1 || got[0].Message != "GET / 200" || got[0].Service != "nginx" || got[0].Source != "file" || got[0].File != path || got[0].Host != "web-1" {
		t.Fatalf("appended %+v", got)
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(" now done\n")
	f.Close()
	s.readFile(path)
	if got := drain(s); len(got) != 1 || got[0].Message != "partial now done" {
		t.Fatalf("finished line %+v", got)
	}
	// copytruncate rotation: start again from the top.
	if err := os.WriteFile(path, []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.readFile(path)
	if got := drain(s); len(got) != 1 || got[0].Message != "fresh" {
		t.Fatalf("after truncate %+v", got)
	}
	s.state.save()
	s2 := testShipper(t, "http://x")
	s2.state = loadLogState(filepath.Dir(s.state.path))
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("after restart\n")
	f.Close()
	s2.readFile(path)
	if got := drain(s2); len(got) != 1 || got[0].Message != "after restart" {
		t.Fatalf("after restart %+v", got)
	}
	if fileService("/var/log/syslog") != "syslog" || fileService("/srv/app/logs/app.log") != "app" || fileService("/var/log/nginx/error.log") != "nginx" {
		t.Fatal("fileService")
	}
}

func TestShipPostsNDJSON(t *testing.T) {
	var got []map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		zr, err := gzip.NewReader(r.Body)
		if err != nil || r.URL.Path != "/api/ingest/logs" || r.Header.Get("Content-Type") != "application/x-ndjson" {
			w.WriteHeader(400)
			return
		}
		sc := bufio.NewScanner(zr)
		for sc.Scan() {
			var m map[string]any
			_ = json.Unmarshal(sc.Bytes(), &m)
			got = append(got, m)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	s := testShipper(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	s.push(&logEntry{Message: "one", Source: "file"})
	s.push(&logEntry{Message: "two", Source: "docker", Container: "api-1", Service: "api"})
	done := make(chan struct{})
	go func() {
		s.ship(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(got) < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if auth != "Bearer k1" || len(got) != 2 || got[0]["message"] != "one" || got[0]["host"] != "web-1" || got[1]["container"] != "api-1" || got[1]["service"] != "api" {
		t.Fatalf("auth %q got %+v", auth, got)
	}
}
