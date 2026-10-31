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
