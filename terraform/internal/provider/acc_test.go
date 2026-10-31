package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Acceptance tests run against a real Visionary: built from this
// repository, started on a free port with a fresh SQLite database and
// sign-in off, then given an admin key. They need TF_ACC=1 and the
// terraform CLI.

var accFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"visionary": providerserver.NewProtocol6WithError(New("test")()),
}

func startVisionary(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}
	if os.Getenv("VISIONARY_URL") != "" && os.Getenv("VISIONARY_API_KEY") != "" {
		// An instance set up by hand.
		return
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "visionary")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "./cmd/visionary")
	build.Dir = filepath.Join("..", "..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build visionary: %v\n%s", err, out)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	srv := exec.Command(bin)
	srv.Dir = dir
	srv.Env = append(os.Environ(), "VISIONARY_ADDR="+addr, "VISIONARY_ENABLE_AUTH=false",
		"VISIONARY_DATABASE_URL=sqlite:///"+filepath.ToSlash(filepath.Join(dir, "acc.db")))
	var logs bytes.Buffer
	srv.Stdout, srv.Stderr = &logs, &logs
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = srv.Process.Kill()
		_ = srv.Wait()
		if t.Failed() {
			t.Logf("visionary output:\n%s", logs.String())
		}
	})
	base := "http://" + addr
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(base + "/api/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("visionary did not start:\n%s", logs.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	resp, err := http.Post(base+"/api/team/keys", "application/json", bytes.NewBufferString(`{"name":"terraform tests","scope":"admin"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var key struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&key); err != nil || key.Secret == "" {
		t.Fatalf("create admin key: %d %v", resp.StatusCode, err)
	}
	t.Setenv("VISIONARY_URL", base)
	t.Setenv("VISIONARY_API_KEY", key.Secret)
}

const accConfig = `
resource "visionary_alert_channel" "ops" {
  name = "ops email"
  kind = "email"
  to   = ["ops@example.com"]
}

resource "visionary_alert_channel" "hook" {
  name   = "deploy hook"
  kind   = "webhook"
  url    = "https://hooks.example.com/visionary/abc123"
  secret = "s3cret-signing-key"
}

resource "visionary_alert_rule" "slow" {
  name      = "Slow checkout"
  severity  = "%s"
  metric    = "latency_p95"
  op        = ">"
  threshold = %d
  window    = "5m"
  for       = "2m"
  channels  = [visionary_alert_channel.ops.id]
  labels    = { team = "payments" }
  scope = {
    route = "/checkout"
  }
  escalation = [{
    after    = "15m"
    channels = [visionary_alert_channel.hook.id]
  }]
}

resource "visionary_alert_rule" "traffic" {
  name        = "Traffic anomaly"
  kind        = "anomaly"
  metric      = "requests_per_min"
  sensitivity = "high"
  depends_on_rules = [visionary_alert_rule.slow.id]
}

resource "visionary_alert_silence" "nights" {
  name       = "Quiet nights"
  severities = ["info", "warning"]
  weekly = {
    days = [1, 2, 3, 4, 5]
    from = "22:00"
    to   = "06:00"
    tz   = "Europe/Berlin"
  }
}

resource "visionary_alert_silence" "maintenance" {
  name     = "Database upgrade"
  rule_ids = [visionary_alert_rule.slow.id]
  once = {
    starts_at = "2030-01-10T22:00:00+01:00"
    ends_at   = "2030-01-11T02:00:00+01:00"
  }
}

resource "visionary_uptime_check" "home" {
  name          = "Home page"
  url           = "https://example.com/"
  interval_s    = %d
  expect_status = "2xx"
  headers       = { Authorization = "Bearer abc", Accept = "text/html" }
  tags          = ["smoke", "deploy"]
}

resource "visionary_uptime_check" "login" {
  name    = "Sign-in flow"
  kind    = "browser"
  secrets = { PASSWORD = "hunter22" }
  steps = [
    { action = "goto", value = "https://example.com/login" },
    { action = "type", selector = "#password", value = "{{secret.PASSWORD}}" },
    { action = "assert_text", text = "Example" },
  ]
}

resource "visionary_uptime_check" "checkout_test" {
  name       = "Checkout pays by card"
  kind       = "playwright"
  interval_s = 1200
  playwright = { project = "chromium", title = "checkout.spec.ts › pays by card" }
}

resource "visionary_dashboard" "main" {
  name        = "Checkout"
  description = "Latency and errors"
  panels = %s
  variables = [{ name = "service", default = "api" }]
  time = { kind = "relative", key = "24h" }
}

resource "visionary_api_key" "ci" {
  name  = "ci deploys"
  scope = "editor"
}

data "visionary_alert_channel" "ops" {
  name       = "ops email"
  depends_on = [visionary_alert_channel.ops]
}

data "visionary_services" "all" {}
`

func TestAccEverything(t *testing.T) {
	startVisionary(t)
	panels := `jsonencode([{ id = "p1", kind = "builtin", ref = "overview/latency", x = 0, y = 0, w = 6, h = 4, viz = { type = "timeseries", unit = "ms" } }])`
	// The same panels written differently: Visionary's answer must not show
	// up as a change after apply or on refresh.
	samePanels := `<<EOT
[
  {"h": 4, "w": 6, "y": 0, "x": 0, "ref": "overview/latency", "kind": "builtin", "id": "p1", "title": "", "viz": {"unit": "ms", "type": "timeseries"}}
]
EOT`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: accFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(accConfig, "warning", 800, 60, panels),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("visionary_alert_channel.ops", "id"),
					resource.TestCheckResourceAttr("visionary_alert_channel.hook", "has_secret", "true"),
					resource.TestCheckResourceAttr("visionary_alert_rule.slow", "kind", "threshold"),
					resource.TestCheckResourceAttr("visionary_alert_rule.slow", "enabled", "true"),
					resource.TestCheckResourceAttr("visionary_alert_rule.slow", "notify_resolved", "true"),
					resource.TestCheckResourceAttrPair("visionary_alert_rule.slow", "channels.0", "visionary_alert_channel.ops", "id"),
					resource.TestCheckResourceAttr("visionary_alert_rule.traffic", "window", "15m"),
					resource.TestCheckResourceAttr("visionary_alert_silence.maintenance", "once.starts_at", "2030-01-10T22:00:00+01:00"),
					resource.TestCheckResourceAttr("visionary_uptime_check.home", "method", "GET"),
					resource.TestCheckTypeSetElemAttr("visionary_uptime_check.home", "tags.*", "smoke"),
					resource.TestCheckResourceAttr("visionary_uptime_check.home", "headers.Authorization", "Bearer abc"),
					resource.TestCheckResourceAttr("visionary_uptime_check.login", "url", "https://example.com/login"),
					resource.TestCheckResourceAttr("visionary_uptime_check.login", "interval_s", "300"),
					resource.TestCheckResourceAttr("visionary_uptime_check.checkout_test", "playwright.project", "chromium"),
					resource.TestMatchResourceAttr("visionary_api_key.ci", "secret", regexp.MustCompile(`^vis_`)),
					resource.TestCheckResourceAttrPair("data.visionary_alert_channel.ops", "id", "visionary_alert_channel.ops", "id"),
					resource.TestCheckResourceAttr("data.visionary_alert_channel.ops", "kind", "email"),
					resource.TestCheckResourceAttrSet("data.visionary_services.all", "id"),
				),
			},
			{
				Config: fmt.Sprintf(accConfig, "warning", 800, 60, samePanels),
			},
			{
				Config: fmt.Sprintf(accConfig, "critical", 900, 120, panels),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("visionary_alert_rule.slow", "severity", "critical"),
					resource.TestCheckResourceAttr("visionary_alert_rule.slow", "threshold", "900"),
					resource.TestCheckResourceAttr("visionary_uptime_check.home", "interval_s", "120"),
				),
			},
			{ResourceName: "visionary_alert_rule.slow", ImportState: true, ImportStateVerify: true},
			{ResourceName: "visionary_alert_rule.traffic", ImportState: true, ImportStateVerify: true},
			{ResourceName: "visionary_alert_silence.nights", ImportState: true, ImportStateVerify: true},
			{ResourceName: "visionary_dashboard.main", ImportState: true, ImportStateVerify: true},
			{ResourceName: "visionary_alert_channel.ops", ImportState: true, ImportStateVerify: true},
			{ResourceName: "visionary_alert_channel.hook", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"url", "secret"}},
			{ResourceName: "visionary_uptime_check.login", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"secrets"}},
			{ResourceName: "visionary_api_key.ci", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"secret"}},
		},
	})
}

func TestAccPlanValidation(t *testing.T) {
	startVisionary(t)
	cases := map[string]string{
		`resource "visionary_alert_rule" "x" {
  name   = "x"
  kind   = "anomaly"
  metric = "rpm"
  op     = ">"
}`: `only for threshold rules`,
		`resource "visionary_alert_rule" "x" {
  name      = "x"
  metric    = "custom"
  op        = ">"
  threshold = 1
}`: `metric custom needs sql`,
		`resource "visionary_uptime_check" "x" {
  name = "x"
}`: `HTTP checks need a url`,
		`resource "visionary_alert_channel" "x" {
  name = "x"
  kind = "slack"
}`: `need the webhook URL`,
		`resource "visionary_dashboard" "x" {
  name   = "x"
  panels = "[{\"id\": \"a\"}]"
}`: `needs an id, a kind`,
		`resource "visionary_alert_silence" "x" {
  name = "x"
}`: `(?s)once.*weekly|weekly.*once`,
	}
	for cfg, want := range cases {
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: accFactories,
			Steps:                    []resource.TestStep{{Config: cfg, PlanOnly: true, ExpectError: regexp.MustCompile(want)}},
		})
	}
}
