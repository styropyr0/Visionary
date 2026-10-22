package visionary

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

// A minimal protobuf encoder for building pprof profiles in tests.
type pb struct{ b []byte }

func (p *pb) varint(x uint64) {
	for x >= 0x80 {
		p.b = append(p.b, byte(x)|0x80)
		x >>= 7
	}
	p.b = append(p.b, byte(x))
}
func (p *pb) key(num, wt int)        { p.varint(uint64(num)<<3 | uint64(wt)) }
func (p *pb) uint(num int, v uint64) { p.key(num, 0); p.varint(v) }
func (p *pb) bytes(num int, b []byte) {
	p.key(num, 2)
	p.varint(uint64(len(b)))
	p.b = append(p.b, b...)
}
func (p *pb) packed(num int, vs ...uint64) {
	var q pb
	for _, v := range vs {
		q.varint(v)
	}
	p.bytes(num, q.b)
}

type testFunc struct {
	name, file string
}

type testLoc struct {
	id    uint64
	lines [][2]uint64 // function id, line; innermost first
}

// buildProfile encodes a profile. Samples are [count, leaf loc, ..., root loc].
// packed chooses the packed or the unpacked encoding of repeated fields.
func buildProfile(funcs []testFunc, locs []testLoc, samples [][]uint64, packed, gz bool) []byte {
	strs := []string{""}
	idx := func(s string) uint64 {
		for i, x := range strs {
			if x == s {
				return uint64(i)
			}
		}
		strs = append(strs, s)
		return uint64(len(strs) - 1)
	}
	var out pb
	for _, s := range samples {
		var m pb
		if packed {
			m.packed(1, s[1:]...)
			m.packed(2, s[0], s[0]*10_000_000)
		} else {
			for _, l := range s[1:] {
				m.uint(1, l)
			}
			m.uint(2, s[0])
			m.uint(2, s[0]*10_000_000)
		}
		out.bytes(2, m.b)
	}
	for _, l := range locs {
		var m pb
		m.uint(1, l.id)
		for _, ln := range l.lines {
			var lm pb
			lm.uint(1, ln[0])
			lm.uint(2, ln[1])
			m.bytes(4, lm.b)
		}
		out.bytes(4, m.b)
	}
	for i, f := range funcs {
		var m pb
		m.uint(1, uint64(i+1))
		m.uint(2, idx(f.name))
		m.uint(4, idx(f.file))
		out.bytes(5, m.b)
	}
	for _, s := range strs {
		out.bytes(6, []byte(s))
	}
	if !gz {
		return out.b
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(out.b)
	_ = zw.Close()
	return buf.Bytes()
}

func testNamer() *frameNamer {
	return &frameNamer{
		goroot:   "/usr/local/go/src/",
		roots:    []string{"/home/me/app/"},
		mainPath: "example.com/app",
	}
}

func TestFrameNames(t *testing.T) {
	n := testNamer()
	cases := []struct {
		fn, file string
		want     string
	}{
		{"main.burn", "/home/me/app/main.go", "main.burn main.go:12"},
		{"example.com/app/handlers.List", "/home/me/app/handlers/list.go", "example.com/app/handlers.List handlers/list.go:12"},
		{"net/http.(*conn).serve", "/usr/local/go/src/net/http/server.go", "net/http.(*conn).serve std:net/http/server.go:12"},
		{"github.com/jackc/pgx/v5.(*Conn).Query", "/root/go/pkg/mod/github.com/jackc/pgx/v5@v5.11.0/conn.go", "github.com/jackc/pgx/v5.(*Conn).Query lib:github.com/jackc/pgx/v5/conn.go:12"},
		{"github.com/BurntSushi/toml.Decode", "/root/go/pkg/mod/github.com/!burnt!sushi/toml@v1.3.0/decode.go", "github.com/BurntSushi/toml.Decode lib:github.com/BurntSushi/toml/decode.go:12"},
		{"runtime.mallocgc", "/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.5.linux-amd64/src/runtime/malloc.go", "runtime.mallocgc std:runtime/malloc.go:12"},
		{"github.com/styropyr0/visionary/sdks/go.(*Agent).Middleware.func1", "/src/visionary/sdks/go/middleware.go", "github.com/styropyr0/visionary/sdks/go.(*Agent).Middleware.func1 lib:github.com/styropyr0/visionary/sdks/go/middleware.go:12"},
		{"github.com/x/y.F", "/home/me/app/vendor/github.com/x/y/f.go", "github.com/x/y.F lib:github.com/x/y/f.go:12"},
		// -trimpath builds
		{"net/http.HandlerFunc.ServeHTTP", "net/http/server.go", "net/http.HandlerFunc.ServeHTTP std:net/http/server.go:12"},
		{"github.com/x/y.F", "github.com/x/y@v1.0.0/f.go", "github.com/x/y.F lib:github.com/x/y/f.go:12"},
		{"example.com/app/db.Q", "example.com/app/db/q.go", "example.com/app/db.Q db/q.go:12"},
		// Built in another GOROOT.
		{"runtime.memmove", "/opt/go/src/runtime/memmove_amd64.s", "runtime.memmove std:runtime/memmove_amd64.s:12"},
		// No file: name alone; ";" never splits a frame.
		{"runtime._GC", "", "runtime._GC"},
		{"main.weird;name", "/home/me/app/main.go", "main.weird,name main.go:12"},
	}
	for _, c := range cases {
		if got := n.frame(ppFrame{fn: c.fn, file: c.file, line: 12}); got != c.want {
			t.Errorf("frame(%s, %s) = %q, want %q", c.fn, c.file, got, c.want)
		}
	}
}

func TestFrameNamesReplaced(t *testing.T) {
	n := testNamer()
	n.replaced = []dirModule{{dir: "/home/me/app/third_party/sdk/", module: "github.com/styropyr0/visionary/sdks/go"}}
	// Inlined into main, the closure carries main's name; the file says whose it is.
	got := n.frame(ppFrame{fn: "main.main.(*Agent).Middleware.func2", file: "/home/me/app/third_party/sdk/middleware.go", line: 209})
	if want := "main.main.(*Agent).Middleware.func2 lib:github.com/styropyr0/visionary/sdks/go/middleware.go:209"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFrameNamesWindows(t *testing.T) {
	n := &frameNamer{goroot: "C:/Go/src/", roots: []string{"E:/Work/App/"}, fold: true}
	if got := n.file(`c:\go\src\net\http\server.go`, "net/http.F"); got != "std:net/http/server.go" {
		t.Errorf("got %q", got)
	}
	if got := n.file(`e:\work\app\api\h.go`, "main.h"); got != "api/h.go" {
		t.Errorf("got %q", got)
	}
}

func foldTestProfile(packed, gz bool) []byte {
	funcs := []testFunc{
		{"runtime.goexit", "/usr/local/go/src/runtime/asm_amd64.s"}, // 1
		{"net/http.(*conn).serve", "/usr/local/go/src/net/http/server.go"},
		{"main.handler", "/home/me/app/main.go"},
		{"main.burn", "/home/me/app/main.go"},                                 // 4
		{"crypto/sha256.Sum256", "/usr/local/go/src/crypto/sha256/sha256.go"}, // 5
		{"github.com/styropyr0/visionary/sdks/go.(*writer).run", "/x/writer.go"},
		{"runtime/pprof.profileWriter", "/usr/local/go/src/runtime/pprof/pprof.go"},
	}
	locs := []testLoc{
		{1, [][2]uint64{{1, 1700}}},
		{2, [][2]uint64{{2, 2100}}},
		// main.burn inlined into main.handler: innermost first.
		{3, [][2]uint64{{4, 20}, {3, 30}}},
		{4, [][2]uint64{{5, 40}}},
		{5, [][2]uint64{{6, 100}}},
		{6, [][2]uint64{{7, 900}}},
	}
	samples := [][]uint64{
		{7, 4, 3, 2, 1}, // sha256 under burn under handler
		{3, 3, 2, 1},    // burn itself
		{5, 5, 1},       // SDK writer goroutine: left out
		{2, 6, 1},       // pprof's writer: left out
	}
	return buildProfile(funcs, locs, samples, packed, gz)
}

func TestFoldProfile(t *testing.T) {
	// Only the sampled (last) frame keeps its line.
	want := "runtime.goexit std:runtime/asm_amd64.s;net/http.(*conn).serve std:net/http/server.go;main.handler main.go;main.burn main.go;crypto/sha256.Sum256 std:crypto/sha256/sha256.go:40 7\n" +
		"runtime.goexit std:runtime/asm_amd64.s;net/http.(*conn).serve std:net/http/server.go;main.handler main.go;main.burn main.go:20 3\n"
	for _, packed := range []bool{true, false} {
		for _, gz := range []bool{true, false} {
			folded, samples, err := foldProfile(foldTestProfile(packed, gz), testNamer())
			if err != nil {
				t.Fatal(err)
			}
			if samples != 10 {
				t.Errorf("packed=%v gz=%v: samples = %d, want 10", packed, gz, samples)
			}
			if folded != want {
				t.Errorf("packed=%v gz=%v: folded =\n%s\nwant\n%s", packed, gz, folded, want)
			}
		}
	}
}

func TestFoldMalformed(t *testing.T) {
	if _, _, err := foldProfile([]byte{0x12, 0x7f, 0x01}, testNamer()); err == nil {
		t.Error("want an error for a truncated message")
	}
}

func TestFoldCap(t *testing.T) {
	lines := map[string]int64{}
	var total int64
	for i := range 3000 {
		k := strings.Repeat("x", 60) + strings.Repeat("y", i%7) + string(rune('a'+i%26)) + strings.Repeat("z", i/26)
		lines[k] += int64(i%50 + 1)
		total += int64(i%50 + 1)
	}
	out := foldLines(lines)
	rows := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	last := rows[len(rows)-1]
	if !strings.HasPrefix(last, "(other) ") {
		t.Fatalf("no (other) line: %q", last)
	}
	if body := len(out) - len(last) - 1; body > profileMaxFolded {
		t.Errorf("folded body %d bytes, over the cap", body)
	}
	// Heaviest first, and every sample accounted for.
	var sum, prev int64 = 0, 1 << 62
	for _, r := range rows {
		i := strings.LastIndexByte(r, ' ')
		var n int64
		for _, c := range r[i+1:] {
			n = n*10 + int64(c-'0')
		}
		if !strings.Contains(r, "(other)") {
			if n > prev {
				t.Fatalf("not heaviest first at %q", r)
			}
			prev = n
		}
		sum += n
	}
	if sum != total {
		t.Errorf("samples %d, want %d", sum, total)
	}
}

// Stacks that don't fit are cut after the deepest frame they share with a
// kept stack, so their time stays under the right caller.
func TestFoldRollsUpIntoCallers(t *testing.T) {
	lines := map[string]int64{}
	heavy := "serve;handler;" + strings.Repeat("h", profileKeepWhole-40)
	lines[heavy] = 100
	lines["serve;handler;json;encode;"+strings.Repeat("e", 30000)] = 7
	lines["serve;handler;json;encode;"+strings.Repeat("f", 30000)] = 5
	lines["serve;handler"] = 4 // leads into the kept stack: kept as it is
	lines["worker;loop;"+strings.Repeat("w", 30000)] = 3
	out := foldLines(lines)
	for _, want := range []string{heavy + " 100\n", "serve;handler;(other) 12\n", "serve;handler 4\n", "(other) 3\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%.300s", want, out)
		}
	}
	if len(out) > profileMaxFolded {
		t.Errorf("%d bytes, over the cap", len(out))
	}
}

// A real profile of this process names the function burning CPU.
func TestFoldRealProfile(t *testing.T) {
	var buf bytes.Buffer
	if err := pprof.StartCPUProfile(&buf); err != nil {
		t.Skip("profiler in use:", err)
	}
	burnCPU(400 * time.Millisecond)
	pprof.StopCPUProfile()
	folded, samples, err := foldProfile(buf.Bytes(), newFrameNamer())
	if err != nil {
		t.Fatal(err)
	}
	if samples == 0 || !strings.Contains(folded, ".burnCPU profile_test.go") {
		t.Fatalf("samples=%d folded:\n%s", samples, folded)
	}
	if !strings.Contains(folded, "std:crypto/") && !strings.Contains(folded, "std:runtime/") {
		t.Errorf("no std: frames:\n%s", folded)
	}
}

var sink [32]byte

func burnCPU(d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		for range 1000 {
			sink = sha256.Sum256(sink[:])
		}
	}
}

func TestParseProfileSettings(t *testing.T) {
	s := parseProfileSettings(nil)
	if s.enabled || s.interval != 300*time.Second || s.duration != 10*time.Second || len(s.services) != 0 {
		t.Errorf("defaults: %+v", s)
	}
	s = parseProfileSettings([][]string{
		{"profile.enabled", "true"}, {"profile.services", `["api","worker"]`},
		{"profile.interval_s", "60"}, {"profile.duration_ms", "2500"}, {"profile.now", "2026-10-22T10:00:00.000Z"},
	})
	if !s.enabled || s.interval != time.Minute || s.duration != 2500*time.Millisecond || s.now != "2026-10-22T10:00:00.000Z" {
		t.Errorf("parsed: %+v", s)
	}
	if !s.includes("api") || s.includes("billing") || s.includes("") {
		t.Errorf("services filter wrong: %v", s.services)
	}
	all := parseProfileSettings([][]string{{"profile.services", "[]"}})
	if !all.includes("") || !all.includes("billing") {
		t.Error("an empty list includes every service")
	}
	bad := parseProfileSettings([][]string{{"profile.enabled", "yes please"}, {"profile.interval_s", "-3"}, {"profile.services", "api"}})
	if bad.enabled || bad.interval != 300*time.Second || len(bad.services) != 0 {
		t.Errorf("bad values: %+v", bad)
	}
}

func TestFreshNow(t *testing.T) {
	start := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	now := start.Add(10 * time.Minute)
	f := func(ts time.Time) string { return ts.Format(tsLayout) }
	if !freshNow(f(now.Add(-time.Minute)), start, now) {
		t.Error("a minute old, after start: fresh")
	}
	if freshNow(f(now.Add(-6*time.Minute)), start, now) {
		t.Error("6 minutes old: stale")
	}
	if freshNow(f(start.Add(-time.Second)), start, start.Add(time.Second)) {
		t.Error("before the SDK started: ignored")
	}
	if !freshNow(now.Add(-time.Minute).Format("2006-01-02 15:04:05"), start, now) {
		t.Error("SQL timestamp format")
	}
	if freshNow("soon", start, now) {
		t.Error("garbage")
	}
}

// fakeStore answers the settings query; nothing else is used.
type fakeStore struct {
	store
	data [][]string
	err  error
}

func (f *fakeStore) rows(context.Context, string, ...any) ([][]string, error) {
	return f.data, f.err
}
func (f *fakeStore) stringParams() bool { return false }

func TestProfilerSettings(t *testing.T) {
	st := &fakeStore{err: errors.New("no such table: visionary_settings")}
	var logs bytes.Buffer
	p := newProfiler(identity{service: "api", instance: "i"}, st, slog.New(slog.NewTextHandler(&logs, nil)), 30)
	ctx := context.Background()
	now := time.Now()

	p.poll(ctx, now)
	p.poll(ctx, now)
	if p.on || strings.Count(logs.String(), "unavailable") != 1 {
		t.Fatalf("missing table: on=%v, logs:\n%s", p.on, logs.String())
	}

	st.err, st.data = nil, [][]string{{"profile.enabled", "false"}}
	p.poll(ctx, now)
	if p.on {
		t.Fatal("enabled=false: off")
	}

	st.data = [][]string{{"profile.enabled", "true"}, {"profile.interval_s", "5"}}
	p.poll(ctx, now)
	if !p.on || !p.next.Equal(now.Add(5*time.Second)) || !strings.Contains(logs.String(), "CPU profiling on") {
		t.Fatalf("on: %v next %v logs:\n%s", p.on, p.next, logs.String())
	}

	st.data = [][]string{{"profile.enabled", "true"}, {"profile.services", `["worker"]`}}
	p.poll(ctx, now)
	if p.on || !strings.Contains(logs.String(), "CPU profiling off") {
		t.Fatal("api is not listed: off")
	}

	// profile.now: fresh and new arms one profile; the same value again does not.
	ts := now.Add(time.Second).UTC().Format(tsLayout)
	st.data = [][]string{{"profile.now", ts}}
	p.poll(ctx, now.Add(2*time.Second))
	if !p.oneOff {
		t.Fatal("profile.now should arm a one-off")
	}
	p.oneOff = false
	p.poll(ctx, now.Add(3*time.Second))
	if p.oneOff {
		t.Fatal("an unchanged profile.now is not a new request")
	}
	// ... but not for a service the list leaves out.
	st.data = [][]string{{"profile.now", now.Add(4 * time.Second).UTC().Format(tsLayout)}, {"profile.services", `["worker"]`}}
	p.poll(ctx, now.Add(5*time.Second))
	if p.oneOff {
		t.Fatal("profile.now honours profile.services")
	}
	// A stale one is ignored.
	st.data = [][]string{{"profile.now", now.Add(-time.Hour).UTC().Format(tsLayout)}}
	p.poll(ctx, now.Add(6*time.Second))
	if p.oneOff {
		t.Fatal("a stale profile.now is ignored")
	}
}

func TestProfileOffEnv(t *testing.T) {
	t.Setenv("VISIONARY_PROFILE", "false")
	t.Setenv("D1_ACCOUNT_ID", "a")
	t.Setenv("D1_DATABASE_ID", "d")
	t.Setenv("D1_API_TOKEN", "t")
	cfg := FromEnv()
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	if !cfg.ProfileOff {
		t.Fatal("VISIONARY_PROFILE=false sets ProfileOff")
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if a.profiler != nil {
		t.Fatal("no profiler with VISIONARY_PROFILE=false")
	}
	t.Setenv("VISIONARY_PROFILE", "")
	cfg = FromEnv()
	if a, _ = New(cfg); a.profiler == nil {
		t.Fatal("profiler follows the dashboard by default")
	}
}

// The middleware tallies requests and, while a profile runs, route time.
func TestProfilerTally(t *testing.T) {
	p := newProfiler(identity{}, &fakeStore{}, slog.Default(), 0)
	p.requestStarted()
	p.requestFinished("GET /x", time.Second) // not collecting: ignored
	p.busy = map[string]float64{}
	p.collecting.Store(true)
	p.requestFinished("GET /x", 1500*time.Microsecond)
	p.requestFinished("GET /x", 500*time.Microsecond)
	if p.started.Load() != 1 || p.busy["GET /x"] != 2 {
		t.Fatalf("started %d busy %v", p.started.Load(), p.busy)
	}
}

// Retention is the server's job: FromEnv leaves the SDK's own purge off.
func TestRetentionOffByDefault(t *testing.T) {
	t.Setenv("VISIONARY_LOG_RETENTION_DAYS", "")
	t.Setenv("VISIONARY_SYSTEM_RETENTION_DAYS", "")
	cfg := FromEnv()
	if cfg.Requests.RetentionDays != 0 || cfg.System.RetentionDays != 0 {
		t.Fatalf("retention defaults to 0, got %d and %d", cfg.Requests.RetentionDays, cfg.System.RetentionDays)
	}
	t.Setenv("VISIONARY_LOG_RETENTION_DAYS", "90")
	t.Setenv("VISIONARY_SYSTEM_RETENTION_DAYS", "7")
	cfg = FromEnv()
	if cfg.Requests.RetentionDays != 90 || cfg.System.RetentionDays != 7 {
		t.Fatalf("retention follows the env, got %d and %d", cfg.Requests.RetentionDays, cfg.System.RetentionDays)
	}
}
