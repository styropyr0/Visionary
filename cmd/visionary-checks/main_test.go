package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeServer answers a run that finishes on the second poll.
func fakeServer(t *testing.T, final string) (*httptest.Server, *runRequest) {
	var got runRequest
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer vis_test" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid, expired or revoked API key"}`))
			return
		}
		ms := 182.0
		b := batch{ID: 7, Status: "running", Tags: []string{"smoke"}, Total: 2, CreatedAt: "2026-10-31T10:00:00.000Z",
			Items: []item{{CheckID: 1, CheckName: "Home", Kind: "http", Status: "passed", DurationMs: &ms}, {CheckID: 2, CheckName: "Checkout", Kind: "browser", Status: "running"}}}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/uptime/runs":
			_ = json.NewDecoder(r.Body).Decode(&got)
		case r.Method == http.MethodGet && r.URL.Path == "/api/uptime/runs/7":
			if polls.Add(1) >= 2 {
				b.Done, b.Status = true, final
				b.Items[1].Status, b.Items[1].DurationMs = final, &ms
				b.Passed = 1
				if final == "failed" {
					b.Failed = 1
					b.Items[1].Error = "text not shown"
					step := 1
					b.Items[1].Detail.FailedStep = &step
					b.Items[1].Detail.Steps = append(b.Items[1].Detail.Steps, struct {
						Label  string `json:"label"`
						Status string `json:"status"`
					}{"open", "passed"}, struct {
						Label  string `json:"label"`
						Status string `json:"status"`
					}{"cart shows total", "failed"})
				} else {
					b.Passed = 2
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"batch": b})
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestRunFailsAndWritesJUnit(t *testing.T) {
	srv, got := fakeServer(t, "failed")
	junit := filepath.Join(t.TempDir(), "checks.xml")
	var out, errOut bytes.Buffer
	t.Setenv("GITHUB_SHA", "abcdef1234567890")
	code := run([]string{"run", "--server", srv.URL, "--key", "vis_test", "--tag", "smoke,checkout", "--url-override", "https://pr-1.example.com", "--junit", junit}, &out, &errOut)
	if code != exitFailed {
		t.Fatalf("exit %d, stderr %s", code, errOut.String())
	}
	if strings.Join(got.Tags, ",") != "smoke,checkout" || got.Commit != "abcdef1234567890" || got.EnvironmentURLOverride != "https://pr-1.example.com" || !got.Wait {
		t.Fatalf("request %+v", *got)
	}
	s := out.String()
	for _, want := range []string{"Visionary run #7", "PASS   Home", "FAIL   Checkout", "cart shows total: text not shown", "2 checks: 1 passed, 1 failed"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary lacks %q:\n%s", want, s)
		}
	}
	raw, err := os.ReadFile(junit)
	if err != nil {
		t.Fatal(err)
	}
	var doc junitSuites
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if doc.Tests != 2 || doc.Failures != 1 || doc.Suites[0].Cases[1].Failure == nil {
		t.Fatalf("junit %s", raw)
	}
}

func TestRunPasses(t *testing.T) {
	srv, _ := fakeServer(t, "passed")
	var out, errOut bytes.Buffer
	if code := run([]string{"run", "--server", srv.URL + "/", "--key", "vis_test", "--check", "1", "--check", "2"}, &out, &errOut); code != exitPassed {
		t.Fatalf("exit %d: %s %s", code, out.String(), errOut.String())
	}
}

func TestRunUsageAndAuth(t *testing.T) {
	srv, _ := fakeServer(t, "passed")
	var out, errOut bytes.Buffer
	if code := run([]string{"run", "--server", srv.URL, "--key", "vis_test"}, &out, &errOut); code != exitUsage {
		t.Fatalf("no tag: exit %d", code)
	}
	errOut.Reset()
	if code := run([]string{"run", "--server", srv.URL, "--key", "vis_wrong", "--tag", "x"}, &out, &errOut); code != exitUsage || !strings.Contains(errOut.String(), "revoked API key") {
		t.Fatalf("bad key: exit %d %s", code, errOut.String())
	}
}
