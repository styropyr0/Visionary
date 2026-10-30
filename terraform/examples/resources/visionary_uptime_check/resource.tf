resource "visionary_uptime_check" "api" {
  name          = "API health"
  url           = "https://api.example.com/healthz"
  interval_s    = 60
  expect_status = "200"
  body_contains = "ok"
  degraded_ms   = 800
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

variable "health_token" {
  type      = string
  sensitive = true
}

variable "probe_password" {
  type      = string
  sensitive = true
}
