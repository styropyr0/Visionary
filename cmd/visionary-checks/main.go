// Command visionary-checks runs Visionary uptime checks from CI: it starts a
// run of the checks with a tag (or ids), waits for it, prints a summary and
// exits non-zero when a check failed. It can also write JUnit XML, so CI test
// reports list each check.
//
//	visionary-checks run --tag smoke --url-override https://pr-12.example.com --junit checks.xml
//
// The server and key come from --server and --key, or VISIONARY_URL and
// VISIONARY_KEY. Use a key with the CI scope (Settings → API keys).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

// Exit codes.
const (
	exitPassed  = 0
	exitFailed  = 1
	exitUsage   = 2
	exitTimeout = 3
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "run":
		return runChecks(args[1:], stdout, stderr)
	case "version", "--version", "-version":
		fmt.Fprintln(stdout, "visionary-checks", version)
		return exitPassed
	case "help", "-h", "--help", "-help":
		usage(stdout)
		return exitPassed
	}
	fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
	usage(stderr)
	return exitUsage
}

func usage(w io.Writer) {
	fmt.Fprint(w, `visionary-checks runs Visionary uptime checks from CI.

Usage:
  visionary-checks run --tag smoke [flags]
  visionary-checks version

Run "visionary-checks run -h" for the flags.
`)
}

// listFlag collects a repeatable, comma separated flag.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

type options struct {
	server, key, release, commit, override, junit string
	tags, checks                                  listFlag
	wait, jsonOut                                 bool
	timeout                                       time.Duration
}

func runChecks(args []string, stdout, stderr io.Writer) int {
	var o options
	fs := flag.NewFlagSet("visionary-checks run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.server, "server", os.Getenv("VISIONARY_URL"), "Visionary's address, e.g. https://visionary.example.com (env VISIONARY_URL)")
	fs.StringVar(&o.key, "key", os.Getenv("VISIONARY_KEY"), "API key with the CI scope (env VISIONARY_KEY)")
	fs.Var(&o.tags, "tag", "run the checks with this tag; repeat or comma separate for several")
	fs.Var(&o.checks, "check", "run the check with this id; repeat or comma separate for several")
	fs.BoolVar(&o.wait, "wait", true, "wait for the results; with --wait=false, print the run id and exit")
	fs.DurationVar(&o.timeout, "timeout", 10*time.Minute, "how long to wait for the results")
	fs.StringVar(&o.release, "release", firstEnv("VISIONARY_RELEASE"), "release the run tested, shown with the results (env VISIONARY_RELEASE)")
	fs.StringVar(&o.commit, "commit", firstEnv("VISIONARY_COMMIT", "GITHUB_SHA", "CI_COMMIT_SHA", "BUILDKITE_COMMIT", "CIRCLE_SHA1", "GIT_COMMIT"),
		"commit the run tested, which links it to the CI page (default: the CI's commit variable)")
	fs.StringVar(&o.override, "url-override", "", "run the checks against this origin instead of their own, e.g. a preview deployment")
	fs.StringVar(&o.junit, "junit", "", "write the results as JUnit XML to this file")
	fs.BoolVar(&o.jsonOut, "json", false, "print the run as JSON instead of a summary")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitPassed
		}
		return exitUsage
	}
	o.server = strings.TrimRight(strings.TrimSpace(o.server), "/")
	switch {
	case o.server == "":
		fmt.Fprintln(stderr, "set --server or VISIONARY_URL")
		return exitUsage
	case o.key == "":
		fmt.Fprintln(stderr, "set --key or VISIONARY_KEY (an API key with the CI scope)")
		return exitUsage
	case len(o.tags) == 0 && len(o.checks) == 0:
		fmt.Fprintln(stderr, "pick checks with --tag or --check")
		return exitUsage
	}
	ids := make([]int64, 0, len(o.checks))
	for _, c := range o.checks {
		id, err := strconv.ParseInt(c, 10, 64)
		if err != nil || id <= 0 {
			fmt.Fprintf(stderr, "--check %q is not a check id\n", c)
			return exitUsage
		}
		ids = append(ids, id)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	deadline := time.Now().Add(o.timeout)
	c := &client{server: o.server, key: o.key, http: &http.Client{Timeout: 4 * time.Minute}}
	b, err := c.start(ctx, runRequest{Tags: o.tags, CheckIDs: ids, Wait: o.wait, TimeoutS: waitSeconds(deadline), Release: o.release,
		Commit: o.commit, EnvironmentURLOverride: o.override})
	if err != nil {
		fmt.Fprintln(stderr, "visionary-checks:", err)
		return exitUsage
	}
	if !o.wait {
		fmt.Fprintf(stdout, "Started run #%d with %d checks: %s\n", b.ID, b.Total, c.link())
		return exitPassed
	}
	for !b.Done && time.Now().Before(deadline) && ctx.Err() == nil {
		next, err := c.get(ctx, b.ID, waitSeconds(deadline))
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			fmt.Fprintln(stderr, "visionary-checks:", err)
			time.Sleep(2 * time.Second)
			continue
		}
		b = next
	}
	if o.junit != "" {
		if err := os.WriteFile(o.junit, junitXML(b), 0o644); err != nil {
			fmt.Fprintln(stderr, "visionary-checks: writing JUnit:", err)
		}
	}
	if o.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(b)
	} else {
		summary(stdout, b, c.link())
	}
	switch {
	case !b.Done:
		fmt.Fprintf(stderr, "visionary-checks: run #%d did not finish within %s\n", b.ID, o.timeout)
		return exitTimeout
	case b.Status != "passed":
		return exitFailed
	}
	return exitPassed
}

// waitSeconds is how long one long poll may wait: the server holds a request
// for at most 170 seconds.
func waitSeconds(deadline time.Time) int {
	return max(1, min(170, int(time.Until(deadline).Seconds())))
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

type runRequest struct {
	Tags                   []string `json:"tags,omitempty"`
	CheckIDs               []int64  `json:"check_ids,omitempty"`
	Wait                   bool     `json:"wait"`
	TimeoutS               int      `json:"timeout_s,omitempty"`
	Release                string   `json:"release,omitempty"`
	Commit                 string   `json:"commit,omitempty"`
	EnvironmentURLOverride string   `json:"environment_url_override,omitempty"`
}

type batch struct {
	ID             int64    `json:"id"`
	CreatedAt      string   `json:"created_at"`
	FinishedAt     string   `json:"finished_at,omitempty"`
	Status         string   `json:"status"`
	Done           bool     `json:"done"`
	Release        string   `json:"release,omitempty"`
	Commit         string   `json:"commit,omitempty"`
	EnvironmentURL string   `json:"environment_url,omitempty"`
	Tags           []string `json:"tags"`
	Total          int      `json:"total"`
	Passed         int      `json:"passed"`
	Failed         int      `json:"failed"`
	Skipped        int      `json:"skipped"`
	Items          []item   `json:"items"`
}

type item struct {
	CheckID    int64    `json:"check_id"`
	CheckName  string   `json:"check_name"`
	Kind       string   `json:"kind"`
	Status     string   `json:"status"`
	URL        string   `json:"url,omitempty"`
	DurationMs *float64 `json:"duration_ms"`
	HTTPStatus int      `json:"http_status,omitempty"`
	Error      string   `json:"error,omitempty"`
	Detail     struct {
		Steps []struct {
			Label  string `json:"label"`
			Status string `json:"status"`
		} `json:"steps"`
		FailedStep *int `json:"failed_step"`
	} `json:"detail"`
}

// failedStep names the browser step that failed, if any.
func (it item) failedStep() string {
	if f := it.Detail.FailedStep; f != nil && *f >= 0 && *f < len(it.Detail.Steps) {
		return it.Detail.Steps[*f].Label
	}
	return ""
}

type client struct {
	server, key string
	http        *http.Client
}

func (c *client) link() string { return c.server + "/#/uptime" }

func (c *client) start(ctx context.Context, req runRequest) (batch, error) {
	body, _ := json.Marshal(req)
	return c.do(ctx, http.MethodPost, "/api/uptime/runs", body)
}

func (c *client) get(ctx context.Context, id int64, waitS int) (batch, error) {
	return c.do(ctx, http.MethodGet, fmt.Sprintf("/api/uptime/runs/%d?wait=1&timeout_s=%d", id, waitS), nil)
}

func (c *client) do(ctx context.Context, method, path string, body []byte) (batch, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.server+path, bytes.NewReader(body))
	if err != nil {
		return batch{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "visionary-checks/"+version)
	resp, err := c.http.Do(req)
	if err != nil {
		return batch{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return batch{}, err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return batch{}, fmt.Errorf("%s (HTTP %d)", e.Error, resp.StatusCode)
		}
		return batch{}, fmt.Errorf("HTTP %d from %s", resp.StatusCode, c.server)
	}
	var out struct {
		Batch batch `json:"batch"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return batch{}, fmt.Errorf("unexpected answer from %s: %w", c.server, err)
	}
	return out.Batch, nil
}

var statusWord = map[string]string{"passed": "PASS", "failed": "FAIL", "error": "ERROR", "skipped": "SKIP", "queued": "WAIT", "running": "WAIT"}

func summary(w io.Writer, b batch, link string) {
	head := fmt.Sprintf("Visionary run #%d", b.ID)
	if len(b.Tags) > 0 {
		head += " · " + strings.Join(b.Tags, ", ")
	}
	if b.EnvironmentURL != "" {
		head += " · against " + b.EnvironmentURL
	}
	fmt.Fprintln(w, head)
	width := 0
	for _, it := range b.Items {
		width = max(width, len(it.CheckName))
	}
	width = min(width, 48)
	for _, it := range b.Items {
		word := statusWord[it.Status]
		if word == "" {
			word = strings.ToUpper(it.Status)
		}
		line := fmt.Sprintf("  %-5s  %-*s  %8s", word, width, clip(it.CheckName, width), duration(it.DurationMs, it.Status))
		if it.Error != "" {
			msg := it.Error
			if s := it.failedStep(); s != "" {
				msg = s + ": " + msg
			}
			line += "  " + msg
		}
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
	tail := fmt.Sprintf("%d checks: %d passed", b.Total, b.Passed)
	if b.Failed > 0 {
		tail += fmt.Sprintf(", %d failed", b.Failed)
	}
	if b.Skipped > 0 {
		tail += fmt.Sprintf(", %d skipped", b.Skipped)
	}
	if !b.Done {
		tail += ", still running"
	}
	fmt.Fprintln(w, tail+" · "+link)
}

func duration(ms *float64, status string) string {
	if ms == nil || status == "queued" || status == "running" || status == "skipped" {
		return ""
	}
	if *ms < 1000 {
		return fmt.Sprintf("%.0f ms", *ms)
	}
	return fmt.Sprintf("%.1f s", *ms/1000)
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
