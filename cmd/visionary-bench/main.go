// Command visionary-bench measures how much load a database behind Visionary can take
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type options struct {
	db       string
	rate     int
	duration time.Duration
	workers  int
	spans    int
	logs     int
	errPct   float64
	bodies   bool
	drain    time.Duration
	batch    int
	server   string
	token    string
	reads    int
	ranges   string
	out      string
	label    string
}

func main() {
	var o options
	var readOnly, writeOnly bool
	flag.StringVar(&o.db, "db", os.Getenv("VISIONARY_DATABASE_URL"), "database URL the SDK writes to (default $VISIONARY_DATABASE_URL)")
	flag.IntVar(&o.rate, "rate", 500, "requests a second to generate")
	flag.DurationVar(&o.duration, "duration", 30*time.Second, "how long to generate load")
	flag.IntVar(&o.workers, "workers", 64, "goroutines serving generated requests")
	flag.IntVar(&o.spans, "spans", 3, "spans per request")
	flag.IntVar(&o.logs, "logs", 2, "log lines per request")
	flag.Float64Var(&o.errPct, "errors", 1, "percent of requests that fail with a captured error")
	flag.BoolVar(&o.bodies, "bodies", true, "send JSON bodies, which the SDK stores")
	flag.IntVar(&o.batch, "batch", 0, "SDK batch size (default: the SDK's)")
	flag.DurationVar(&o.drain, "drain", 2*time.Minute, "longest wait for queued rows after the load stops")
	flag.StringVar(&o.server, "server", "", "Visionary server for the read phase, e.g. http://localhost:8090")
	flag.StringVar(&o.token, "token", os.Getenv("VISIONARY_BENCH_TOKEN"), "API key for the read phase (not needed with sign-in off)")
	flag.IntVar(&o.reads, "reads", 5, "times each query runs; the median and slowest are reported")
	flag.StringVar(&o.ranges, "ranges", "1h,24h,7d", "time ranges the read phase asks for")
	flag.StringVar(&o.out, "out", "", "also append the Markdown to this file")
	flag.StringVar(&o.label, "label", "", "label printed above the tables")
	flag.BoolVar(&readOnly, "read-only", false, "skip the write phase")
	flag.BoolVar(&writeOnly, "write-only", false, "skip the read phase")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	ctx := context.Background()
	var md strings.Builder
	fmt.Fprintf(&md, "\n### %s\n\n", firstNonEmpty(o.label, "visionary-bench "+time.Now().UTC().Format(time.RFC3339)))
	fmt.Fprintf(&md, "%s, %d CPUs, %s/%s\n", cpuName(), runtime.NumCPU(), runtime.GOOS, runtime.GOARCH)

	if !readOnly {
		if o.db == "" {
			fail("set -db or VISIONARY_DATABASE_URL")
		}
		res, err := runWrite(ctx, o)
		if err != nil {
			fail(err.Error())
		}
		md.WriteString(res.markdown(o))
	}
	if !writeOnly && o.server != "" {
		res, err := runRead(ctx, o)
		if err != nil {
			fail(err.Error())
		}
		md.WriteString(res)
	}
	fmt.Print(md.String())
	if o.out != "" {
		f, err := os.OpenFile(o.out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fail(err.Error())
		}
		_, _ = io.WriteString(f, md.String())
		_ = f.Close()
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "visionary-bench:", msg)
	os.Exit(1)
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func cpuName() string {
	if v := os.Getenv("PROCESSOR_IDENTIFIER"); v != "" {
		return v
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if name, ok := strings.CutPrefix(line, "model name"); ok {
				return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), ":"))
			}
		}
	}
	return "unknown CPU"
}
