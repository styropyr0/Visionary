package analytics

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const ghSecret = "It's a Secret to Everybody"

const ghRunRequested = `{
  "action": "requested",
  "workflow_run": {
    "id": 30433642, "name": "Build", "node_id": "MDEyOldvcmtmbG93IFJ1bjI2OTI4OQ==",
    "head_branch": "main", "head_sha": "acb5820ced9479c074f688cc328bf03f341a511d",
    "path": ".github/workflows/build.yml", "display_title": "Update README.md",
    "run_number": 562, "event": "push", "status": "queued", "conclusion": null,
    "workflow_id": 159038, "check_suite_id": 414944374, "url": "https://api.github.com/repos/octo-org/octo-repo/actions/runs/30433642",
    "html_url": "https://github.com/octo-org/octo-repo/actions/runs/30433642",
    "pull_requests": [], "created_at": "2026-10-28T10:00:00Z", "updated_at": "2026-10-28T10:00:00Z",
    "actor": {"login": "octocat", "id": 1, "type": "User"},
    "run_attempt": 1, "run_started_at": "2026-10-28T10:00:00Z",
    "triggering_actor": {"login": "octocat", "id": 1, "type": "User"},
    "head_commit": {"id": "acb5820ced9479c074f688cc328bf03f341a511d", "message": "Update README.md\n\nLonger body", "timestamp": "2026-10-28T09:59:50Z",
      "author": {"name": "Octo Cat", "email": "octocat@github.com"}}
  },
  "workflow": {"id": 159038, "name": "Build", "path": ".github/workflows/build.yml", "state": "active"},
  "repository": {"id": 186853002, "name": "octo-repo", "full_name": "octo-org/octo-repo", "private": false, "default_branch": "main"},
  "sender": {"login": "octocat", "id": 1}
}`

const ghRunCompleted = `{
  "action": "completed",
  "workflow_run": {
    "id": 30433642, "name": "Build", "head_branch": "main", "head_sha": "acb5820ced9479c074f688cc328bf03f341a511d",
    "run_number": 562, "event": "push", "status": "completed", "conclusion": "failure",
    "html_url": "https://github.com/octo-org/octo-repo/actions/runs/30433642",
    "created_at": "2026-10-28T10:00:00Z", "updated_at": "2026-10-28T10:07:30Z",
    "actor": {"login": "octocat"}, "run_attempt": 1, "run_started_at": "2026-10-28T10:00:00Z",
    "triggering_actor": {"login": "octocat"}, "head_commit": {"message": "Update README.md"}
  },
  "repository": {"name": "octo-repo", "full_name": "octo-org/octo-repo", "default_branch": "main"}
}`

const ghJobInProgress = `{
  "action": "in_progress",
  "workflow_job": {
    "id": 2832853555, "run_id": 30433642, "workflow_name": "Build", "head_branch": "main",
    "run_url": "https://api.github.com/repos/octo-org/octo-repo/actions/runs/30433642", "run_attempt": 1,
    "head_sha": "acb5820ced9479c074f688cc328bf03f341a511d", "html_url": "https://github.com/octo-org/octo-repo/actions/runs/30433642/job/2832853555",
    "status": "in_progress", "conclusion": null, "created_at": "2026-10-28T10:00:05Z", "started_at": "2026-10-28T10:00:35Z", "completed_at": null,
    "name": "test (ubuntu-latest)", "steps": [{"name": "Set up job", "status": "completed", "conclusion": "success", "number": 1,
      "started_at": "2026-10-28T10:00:35Z", "completed_at": "2026-10-28T10:00:37Z"}],
    "labels": ["ubuntu-latest"], "runner_id": 1, "runner_name": "GitHub Actions 2", "runner_group_name": "GitHub Actions"
  },
  "repository": {"name": "octo-repo", "full_name": "octo-org/octo-repo", "default_branch": "main"}
}`

const ghJobCompleted = `{
  "action": "completed",
  "workflow_job": {
    "id": 2832853555, "run_id": 30433642, "workflow_name": "Build", "head_branch": "main", "run_attempt": 1,
    "head_sha": "acb5820ced9479c074f688cc328bf03f341a511d", "html_url": "https://github.com/octo-org/octo-repo/actions/runs/30433642/job/2832853555",
    "status": "completed", "conclusion": "failure", "created_at": "2026-10-28T10:00:05Z", "started_at": "2026-10-28T10:00:35Z",
    "completed_at": "2026-10-28T10:07:10Z", "name": "test (ubuntu-latest)",
    "steps": [
      {"name": "Set up job", "status": "completed", "conclusion": "success", "number": 1, "started_at": "2026-10-28T10:00:35Z", "completed_at": "2026-10-28T10:00:37Z"},
      {"name": "Run tests", "status": "completed", "conclusion": "failure", "number": 2, "started_at": "2026-10-28T10:00:37Z", "completed_at": "2026-10-28T10:07:05Z"},
      {"name": "Upload results", "status": "completed", "conclusion": "skipped", "number": 3, "started_at": "2026-10-28T10:07:05Z", "completed_at": "2026-10-28T10:07:05Z"}
    ],
    "runner_name": "GitHub Actions 2"
  },
  "repository": {"name": "octo-repo", "full_name": "octo-org/octo-repo", "default_branch": "main"}
}`

func ghSign(body string) string {
	mac := hmac.New(sha256.New, []byte(ghSecret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func ciMux(s *Server) http.Handler {
	mux := http.NewServeMux()
	as := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Name: "Ann", Role: "viewer"})))
		}
	}
	mux.HandleFunc("POST /api/ingest/ci/github", s.handleGitHubWebhook)
	mux.HandleFunc("POST /api/ingest/ci/runs", s.handleCIRuns)
	mux.HandleFunc("POST /api/ingest/ci/junit", s.handleJUnit)
	mux.HandleFunc("GET /api/ci/summary", as(s.handleCISummary))
	mux.HandleFunc("GET /api/ci/runs", as(s.handleCIRunList))
	mux.HandleFunc("GET /api/ci/runs/{id}", as(s.handleCIRun))
	mux.HandleFunc("GET /api/ci/tests", as(s.handleCITests))
	mux.HandleFunc("GET /api/ci/flaky", as(s.handleCIFlaky))
	mux.HandleFunc("GET /api/ci/builds", as(s.handleCIBuilds))
	return mux
}

func ghDeliver(t *testing.T, h http.Handler, event, body, sig string, want int) {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/ingest/ci/github?service=shop", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-GitHub-Event", event)
	if sig != "" {
		r.Header.Set("X-Hub-Signature-256", sig)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s delivery = %d %s, want %d", event, w.Code, w.Body, want)
	}
}

func ciGet(t *testing.T, h http.Handler, path string, dst any) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubWebhookSignature(t *testing.T) {
	s := logsTestServer(t, nil)
	h := ciMux(s)
	ghDeliver(t, h, "workflow_run", ghRunRequested, ghSign(ghRunRequested), http.StatusForbidden)

	s.live().Config["github_webhook_secret"] = ghSecret
	ghDeliver(t, h, "workflow_run", ghRunRequested, "", http.StatusUnauthorized)
	ghDeliver(t, h, "workflow_run", ghRunRequested, "sha256=0000", http.StatusUnauthorized)
	ghDeliver(t, h, "workflow_run", ghRunRequested+" ", ghSign(ghRunRequested), http.StatusUnauthorized)
	ghDeliver(t, h, "ping", `{"zen":"Keep it logically awesome."}`, ghSign(`{"zen":"Keep it logically awesome."}`), http.StatusOK)
	ghDeliver(t, h, "push", `{"ref":"refs/heads/main"}`, ghSign(`{"ref":"refs/heads/main"}`), http.StatusAccepted)
	ghDeliver(t, h, "workflow_run", ghRunRequested, ghSign(ghRunRequested), http.StatusAccepted)

	if !ghSignatureValid(ghSecret, []byte("Hello, World!"), "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17") {
		t.Fatal("GitHub's documented signature does not verify")
	}
}

func TestGitHubWebhookRunAndJobs(t *testing.T) {
	s := logsTestServer(t, nil)
	s.live().Config["github_webhook_secret"] = ghSecret
	h := ciMux(s)
	ctx := context.Background()
	ghDeliver(t, h, "workflow_run", ghRunRequested, ghSign(ghRunRequested), http.StatusAccepted)
	ghDeliver(t, h, "workflow_job", ghJobCompleted, ghSign(ghJobCompleted), http.StatusAccepted)
	ghDeliver(t, h, "workflow_run", ghRunCompleted, ghSign(ghRunCompleted), http.StatusAccepted)
	ghDeliver(t, h, "workflow_job", ghJobInProgress, ghSign(ghJobInProgress), http.StatusAccepted)
	ghDeliver(t, h, "workflow_run", ghRunRequested, ghSign(ghRunRequested), http.StatusAccepted)

	res, err := s.db.Query(ctx, `SELECT status, conclusion, queue_ms, duration_ms, branch, commit_sha, actor, service, workflow, run_number, commit_message, default_branch FROM ci_pipelines`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("%d runs stored, want 1", len(res.Rows))
	}
	got := res.Rows[0]
	want := []any{"completed", "failure", int64(35000), int64(450000), "main", "acb5820ced9479c074f688cc328bf03f341a511d", "octocat", "shop", "Build", int64(562), "Update README.md", "main"}
	for i := range want {
		if toString(got[i]) != toString(want[i]) {
			t.Errorf("column %d = %v, want %v", i, got[i], want[i])
		}
	}
	res, err = s.db.Query(ctx, `SELECT status, conclusion, queue_ms, duration_ms, steps, run_key FROM ci_jobs`)
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("jobs: %v %v", err, res)
	}
	job := res.Rows[0]
	if toString(job[0]) != "completed" || toString(job[1]) != "failure" || toInt(job[2]) != 30000 || toInt(job[3]) != 395000 {
		t.Errorf("job = %v", job)
	}
	var steps []ciStep
	if err := json.Unmarshal([]byte(toString(job[4])), &steps); err != nil || len(steps) != 3 || steps[1].Conclusion != "failure" {
		t.Errorf("steps = %s", job[4])
	}
	if toString(job[5]) != "github:octo-org/octo-repo:30433642:1" {
		t.Errorf("run_key = %v", job[5])
	}

	var list struct{ Runs []ciRunRow }
	ciGet(t, h, "/api/ci/runs?from=2026-10-27T00:00:00Z&to=2026-10-29T00:00:00Z", &list)
	if len(list.Runs) != 1 {
		t.Fatalf("listed %d runs", len(list.Runs))
	}
	var detail struct {
		Jobs []ciJobRow
	}
	ciGet(t, h, "/api/ci/runs/"+toString(list.Runs[0].ID), &detail)
	if len(detail.Jobs) != 1 || len(detail.Jobs[0].Steps) != 3 {
		t.Fatalf("detail jobs = %+v", detail.Jobs)
	}
}

const junitPytest = `<?xml version="1.0" encoding="utf-8"?><testsuites name="pytest tests"><testsuite name="pytest" errors="1" failures="1" skipped="1" tests="4" time="1.234" timestamp="2026-10-28T10:01:00.000000+00:00" hostname="runner">
<testcase classname="tests.test_cart" name="test_add_item" file="tests/test_cart.py" line="10" time="0.012" />
<testcase classname="tests.test_cart" name="test_total" file="tests/test_cart.py" line="20" time="0.400"><failure message="assert 41 == 42">def test_total():
&gt;       assert cart.total() == 42
E       assert 41 == 42

tests/test_cart.py:22: AssertionError</failure></testcase>
<testcase classname="tests.test_cart" name="test_db" file="tests/test_cart.py" line="30" time="0.001"><error message="failed on setup with &quot;ConnectionRefusedError&quot;">conn refused</error></testcase>
<testcase classname="tests.test_cart" name="test_slow" file="tests/test_cart.py" line="40" time="0.000"><skipped type="pytest.skip" message="slow">tests/test_cart.py:40: slow</skipped></testcase>
</testsuite></testsuites>`

const junitJest = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites name="jest tests" tests="3" failures="1" errors="0" time="2.31">
  <testsuite name="Cart" errors="0" failures="1" skipped="0" timestamp="2026-10-28T10:01:00" time="1.9" tests="2">
    <testcase classname="Cart adds an item" name="Cart adds an item" time="0.004">
    </testcase>
    <testcase classname="Cart applies a discount" name="Cart applies a discount" time="1.502">
      <failure>Error: expect(received).toBe(expected) // Object.is equality

Expected: 90
Received: 100
    at Object.toBe (/home/runner/work/shop/src/cart.test.ts:14:23)</failure>
    </testcase>
  </testsuite>
  <testsuite name="Checkout" errors="0" failures="0" skipped="0" timestamp="2026-10-28T10:01:02" time="0.41" tests="1">
    <testcase classname="Checkout pays" name="Checkout pays" time="0.41" file="src/checkout.test.ts">
    </testcase>
  </testsuite>
</testsuites>`

const junitSurefire = `<?xml version="1.0" encoding="UTF-8"?>
<testsuite xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:noNamespaceSchemaLocation="https://maven.apache.org/surefire/maven-surefire-plugin/xsd/surefire-test-report.xsd" version="3.0" name="com.example.shop.OrderServiceTest" time="1,204.1" tests="3" errors="0" skipped="0" failures="1">
  <properties>
    <property name="java.version" value="21.0.2"/>
  </properties>
  <testcase name="placesOrder" classname="com.example.shop.OrderServiceTest" time="0.051"/>
  <testcase name="refundsOrder" classname="com.example.shop.OrderServiceTest" time="0.112">
    <flakyFailure message="expected: &lt;true&gt; but was: &lt;false&gt;" type="org.opentest4j.AssertionFailedError"><![CDATA[org.opentest4j.AssertionFailedError: expected: <true> but was: <false>
	at com.example.shop.OrderServiceTest.refundsOrder(OrderServiceTest.java:41)]]></flakyFailure>
  </testcase>
  <testcase name="cancelsOrder" classname="com.example.shop.OrderServiceTest" time="1,000.5">
    <failure message="timeout" type="java.util.concurrent.TimeoutException">java.util.concurrent.TimeoutException</failure>
    <system-out><![CDATA[cancelling]]></system-out>
  </testcase>
</testsuite>`

const junitGo = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites tests="3" failures="1">
	<testsuite name="github.com/example/shop/cart" tests="3" failures="1" errors="0" id="0" hostname="runner" time="0.013" timestamp="2026-10-28T10:01:00Z">
		<properties>
			<property name="go.version" value="go1.23.2 linux/amd64"></property>
		</properties>
		<testcase name="TestAdd" classname="github.com/example/shop/cart" time="0.000"></testcase>
		<testcase name="TestTotal" classname="github.com/example/shop/cart" time="0.010">
			<failure message="Failed" type="">    cart_test.go:22: total = 41, want 42</failure>
		</testcase>
		<testcase name="TestSkip" classname="github.com/example/shop/cart" time="0.000">
			<skipped message="    cart_test.go:30: needs a database"></skipped>
		</testcase>
	</testsuite>
</testsuites>`

func TestParseJUnitFlavours(t *testing.T) {
	type want struct {
		tests, failed, errored, skipped int
		name, file                      string
		msg                             string
		maxMs                           float64
	}
	for name, c := range map[string]struct {
		xml  string
		want want
	}{
		"pytest":   {junitPytest, want{4, 1, 1, 1, "tests.test_cart.test_total", "tests/test_cart.py", "assert 41 == 42", 400}},
		"jest":     {junitJest, want{3, 1, 0, 0, "Cart applies a discount", "src/checkout.test.ts", "Expected: 90", 1502}},
		"surefire": {junitSurefire, want{4, 2, 0, 0, "com.example.shop.OrderServiceTest.cancelsOrder", "", "timeout", 1000500}},
		"go":       {junitGo, want{3, 1, 0, 1, "github.com/example/shop/cart.TestTotal", "", "total = 41, want 42", 10}},
	} {
		t.Run(name, func(t *testing.T) {
			cases, err := parseJUnit([]byte(c.xml))
			if err != nil {
				t.Fatal(err)
			}
			var failed, errored, skipped int
			var maxMs float64
			var first *ciTestCase
			files := map[string]bool{}
			for i, tc := range cases {
				switch tc.Status {
				case "failed":
					failed++
				case "error":
					errored++
				case "skipped":
					skipped++
				}
				if ciFailed(tc.Status) && first == nil && tc.Test == c.want.name {
					first = &cases[i]
				}
				if tc.DurationMs != nil && *tc.DurationMs > maxMs {
					maxMs = *tc.DurationMs
				}
				files[tc.File] = true
			}
			if len(cases) != c.want.tests || failed != c.want.failed || errored != c.want.errored || skipped != c.want.skipped {
				t.Fatalf("got %d cases, %d failed, %d errors, %d skipped: %+v", len(cases), failed, errored, skipped, cases)
			}
			if first == nil || !strings.Contains(first.Message, c.want.msg) {
				t.Fatalf("no failure %q with %q in %+v", c.want.name, c.want.msg, cases)
			}
			if c.want.file != "" && !files[c.want.file] {
				t.Errorf("no file %q in %v", c.want.file, files)
			}
			if maxMs != c.want.maxMs {
				t.Errorf("longest = %v ms, want %v", maxMs, c.want.maxMs)
			}
		})
	}
	if _, err := parseJUnit([]byte(`<html><body>404</body></html>`)); err == nil {
		t.Error("an HTML page parsed as a report")
	}
	long := strings.Repeat("x", 5000)
	cases, _ := parseJUnit([]byte(`<testsuite><testcase name="a"><failure message="boom">` + long + `</failure></testcase></testsuite>`))
	if len(cases) != 1 || len(cases[0].Message) > ciMaxMessage+4 {
		t.Errorf("message not clipped: %d bytes", len(cases[0].Message))
	}
}

func junitUpload(t *testing.T, h http.Handler, fields map[string]string, report string, gz bool) map[string]int {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("file", "junit.xml")
	if gz {
		zw := gzip.NewWriter(fw)
		_, _ = zw.Write([]byte(report))
		_ = zw.Close()
	} else {
		_, _ = fw.Write([]byte(report))
	}
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/api/ingest/ci/junit", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Authorization", "Bearer k1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("upload = %d %s", w.Code, w.Body)
	}
	var out map[string]int
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

func TestJUnitUploadAndFlaky(t *testing.T) {
	s := logsTestServer(t, nil)
	h := ciMux(s)
	ctx := context.Background()

	r := httptest.NewRequest("POST", "/api/ingest/ci/junit", strings.NewReader(junitGo))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no key = %d", w.Code)
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(junitGo))
	_ = zw.Close()
	r = httptest.NewRequest("POST", "/api/ingest/ci/junit?service=cart&branch=feature&commit=aaaaaaa1&at=2026-10-20T10:00:00Z", &gz)
	r.Header.Set("Content-Type", "application/xml")
	r.Header.Set("Content-Encoding", "gzip")
	r.Header.Set("X-Visionary-Key", "k1")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"tests":3`) {
		t.Fatalf("raw gzip upload = %d %s", w.Code, w.Body)
	}

	pass := strings.Replace(junitGo, `<failure message="Failed" type="">    cart_test.go:22: total = 41, want 42</failure>`, "", 1)
	got := junitUpload(t, h, map[string]string{"service": "cart", "branch": "feature", "commit": "aaaaaaa1", "at": "2026-10-20T10:05:00Z"}, pass, true)
	if got["failed"] != 0 || got["passed"] != 2 {
		t.Fatalf("counts = %v", got)
	}
	failAdd := strings.Replace(junitGo, `<testcase name="TestAdd" classname="github.com/example/shop/cart" time="0.000"></testcase>`,
		`<testcase name="TestAdd" classname="github.com/example/shop/cart" time="0.000"><failure message="Failed">flake</failure></testcase>`, 1)
	for i, rep := range []string{pass, failAdd, pass, pass, failAdd, pass} {
		junitUpload(t, h, map[string]string{"service": "cart", "branch": "main", "commit": "bbbbbbb" + string(rune('0'+i)),
			"at": time.Date(2026, 10, 21+i, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)}, rep, false)
	}

	res, err := s.db.Query(ctx, `SELECT test, reason, commit_sha FROM ci_flaky_tests ORDER BY test`)
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, row := range res.Rows {
		reasons[toString(row[0])] = toString(row[1])
	}
	if reasons["github.com/example/shop/cart.TestTotal"] != "same_commit" || reasons["github.com/example/shop/cart.TestAdd"] != "flip_flop" || len(reasons) != 2 {
		t.Fatalf("flaky = %v", reasons)
	}

	var flaky struct {
		Tests []flakyTest
		New   int
	}
	ciGet(t, h, "/api/ci/flaky?from=2026-10-19T00:00:00Z&to=2026-10-29T00:00:00Z", &flaky)
	if len(flaky.Tests) != 2 || flaky.New != 2 || flaky.Tests[0].Score <= 0 {
		t.Fatalf("flaky tab = %+v", flaky)
	}
	var tests struct {
		Totals  map[string]int
		Failing *Result
	}
	ciGet(t, h, "/api/ci/tests?from=2026-10-19T00:00:00Z&to=2026-10-29T00:00:00Z&service=cart", &tests)
	if tests.Totals["tests"] != 3 || len(tests.Failing.Rows) != 2 {
		t.Fatalf("tests tab = %+v %+v", tests.Totals, tests.Failing)
	}
}

func TestCIRunsAndDeployLink(t *testing.T) {
	s := logsTestServer(t, nil)
	h := ciMux(s)
	ctx := context.Background()
	body := `{"runs": [
  {"provider": "gitlab", "repo": "shop/api", "workflow": "pipeline", "run_id": 9001, "number": 77, "branch": "main", "default_branch": "main",
   "commit": "9f1029cabcdef0123456789abcdef0123456789a", "status": "success", "service": "api",
   "queued_at": "2026-10-28T09:00:00Z", "started_at": "2026-10-28T09:01:00Z", "finished_at": "2026-10-28T09:11:00Z",
   "jobs": [{"id": 1, "name": "build", "status": "success", "queued_at": "2026-10-28T09:00:00Z", "started_at": "2026-10-28T09:01:00Z", "finished_at": "2026-10-28T09:05:00Z"},
            {"id": 2, "name": "test", "status": "failed", "queued_at": "2026-10-28T09:05:00Z", "started_at": "2026-10-28T09:06:30Z", "finished_at": "2026-10-28T09:11:00Z"}]},
  {"provider": "gitlab", "repo": "shop/api", "workflow": "pipeline", "run_id": "9002", "branch": "main", "commit": "1111111aaaa", "status": "running",
   "queued_at": "2026-10-28T10:00:00Z", "started_at": "2026-10-28T10:00:10Z"}
]}`
	r := httptest.NewRequest("POST", "/api/ingest/ci/runs", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer k1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("runs = %d %s", w.Code, w.Body)
	}
	if err := s.db.Exec(ctx, `INSERT INTO releases (created_at, service, instance, version, sdk, sdk_version, runtime) VALUES (?, 'api', 'i1', 'v1.4.0-3-g9f1029c', 'go', '1', 'go')`,
		"2026-10-28T09:20:00.000Z"); err != nil {
		t.Fatal(err)
	}
	var list struct{ Runs []ciRunRow }
	ciGet(t, h, "/api/ci/runs?from=2026-10-28T00:00:00Z&to=2026-10-29T00:00:00Z", &list)
	if len(list.Runs) != 2 || list.Runs[0].Status != "in_progress" || list.Runs[1].Conclusion != "success" || len(list.Runs[1].Deploys) != 1 {
		t.Fatalf("runs = %+v", list.Runs)
	}
	var builds struct{ Builds map[string]ciRunRow }
	ciGet(t, h, "/api/ci/builds?version=v1.4.0-3-g9f1029c&version=1.0.0", &builds)
	if b, ok := builds.Builds["v1.4.0-3-g9f1029c"]; !ok || b.Number != 77 || len(builds.Builds) != 1 {
		t.Fatalf("builds = %+v", builds.Builds)
	}
	var sum struct {
		Totals   map[string]any
		SlowJobs *Result `json:"slow_jobs"`
		FailJobs *Result `json:"fail_jobs"`
	}
	ciGet(t, h, "/api/ci/summary?from=2026-10-28T00:00:00Z&to=2026-10-29T00:00:00Z", &sum)
	if sum.Totals["runs"] != float64(2) || sum.Totals["success_rate"] != float64(100) || sum.Totals["p50_ms"] != float64(600000) {
		t.Fatalf("totals = %v", sum.Totals)
	}
	if len(sum.SlowJobs.Rows) != 2 || len(sum.FailJobs.Rows) != 1 || sum.FailJobs.Rows[0][0] != "test" || sum.FailJobs.Rows[0][7] != float64(90000) {
		t.Fatalf("jobs = %v / %v", sum.SlowJobs.Rows, sum.FailJobs.Rows)
	}
}

func TestVersionCommits(t *testing.T) {
	for v, want := range map[string]string{
		"9f1029c":                  "9f1029c",
		"v1.4.0-3-g9f1029c":        "9f1029c",
		"1.4.0+9f1029cabcdef":      "9f1029cabcdef",
		"1.4.0":                    "",
		"20261028":                 "",
		"main-acb5820ced9479c074f": "acb5820ced9479c074f",
	} {
		got := strings.Join(versionCommits(v), ",")
		if got != want {
			t.Errorf("versionCommits(%q) = %q, want %q", v, got, want)
		}
	}
}
