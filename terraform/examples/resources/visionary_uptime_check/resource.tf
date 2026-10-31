resource "visionary_uptime_check" "api" {
  name          = "API health"
  url           = "https://api.example.com/healthz"
  interval_s    = 60
  expect_status = "200"
  body_contains = "ok"
  degraded_ms   = 800
  tags          = ["smoke", "api"]
  headers       = { Authorization = "Bearer ${var.health_token}" }

  on_status_page = true
}

# A browser check: steps played in a headless browser.
resource "visionary_uptime_check" "sign_in" {
  name       = "Sign-in flow"
  kind       = "browser"
  interval_s = 300
  secrets    = { PASSWORD = var.probe_password }

  steps = [
    { action = "goto", value = "https://app.example.com/login" },
    { action = "type", selector = "#email", value = "probe@example.com" },
    { action = "type", selector = "#password", value = "{{secret.PASSWORD}}" },
    { action = "click", text = "Sign in" },
    { action = "wait_navigation" },
    { action = "assert_text", text = "Dashboard", label = "signed in" },
  ]
}

# A multi-step API check: sign in, then read with the token.
resource "visionary_uptime_check" "orders_api" {
  name    = "Sign in and list orders"
  kind    = "api"
  secrets = { PASSWORD = var.probe_password }

  api = {
    requests = [
      {
        name      = "Sign in"
        method    = "POST"
        url       = "https://api.example.com/v1/login"
        body_type = "json"
        body      = jsonencode({ user = "probe", password = "{{secret.PASSWORD}}" })
        extract   = [{ var = "token", source = "json", path = "$.data.token" }]
        assert    = [{ source = "status", op = "in", value = "200" }]
      },
      {
        name    = "Orders"
        url     = "https://api.example.com/v1/orders"
        headers = { Authorization = "Bearer {{var.token}}" }
        assert = [
          { source = "json", path = "$.items", op = "exists" },
          { source = "time", op = "lte", value = "800" },
        ]
      },
    ]
  }
}

resource "visionary_uptime_check" "postgres" {
  name = "Postgres primary"
  kind = "tcp"
  tcp  = { host = "db.example.com", port = 5432 }
}

resource "visionary_uptime_check" "mx" {
  name = "Mail exchangers"
  kind = "dns"
  dns  = { name = "example.com", record_type = "MX", match = "contains", values = ["mx1.example.com"] }
}

resource "visionary_uptime_check" "gateway" {
  name = "Office gateway"
  kind = "ping"
  ping = { host = "203.0.113.1", packets = 5, max_loss_pct = 40 }
}

resource "visionary_uptime_check" "certificate" {
  name       = "example.com certificate"
  kind       = "ssl"
  interval_s = 3600
  ssl        = { host = "example.com", warn_days = 21, fail_days = 7, min_tls = "1.2" }
}

# A heartbeat: the job calls heartbeat_path on the server when it runs.
resource "visionary_uptime_check" "backup" {
  name      = "Nightly backup"
  kind      = "heartbeat"
  heartbeat = { cron = "0 3 * * *", timezone = "Europe/Berlin", grace_s = 1800 }
}

output "backup_heartbeat_path" {
  value     = visionary_uptime_check.backup.heartbeat_path
  sensitive = true
}

# A Playwright test your CI runs with visionary-playwright-reporter; declared so it
# is alerted on before its first run, and stale when no run arrives for 20 minutes.
resource "visionary_uptime_check" "checkout_test" {
  name       = "Checkout pays by card"
  kind       = "playwright"
  interval_s = 1200
  tags       = ["checkout"]

  playwright = {
    project = "chromium"
    title   = "checkout.spec.ts › pays by card"
  }
}

variable "health_token" {
  type      = string
  sensitive = true
}

variable "probe_password" {
  type      = string
  sensitive = true
}
