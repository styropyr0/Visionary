# A starting point: a channel, two rules, quiet hours, an uptime check and a
# dashboard. Set VISIONARY_URL and VISIONARY_API_KEY (an editor key), then
# terraform init && terraform apply

terraform {
  required_providers {
    visionary = {
      source = "styropyr0/visionary"
    }
  }
}

provider "visionary" {}

variable "latency_ms" {
  description = "p95 latency that pages."
  type        = number
  default     = 800
}

resource "visionary_alert_channel" "ops" {
  name = "Ops email"
  kind = "email"
  to   = ["ops@example.com"]
}

resource "visionary_alert_rule" "slow" {
  name      = "Slow API"
  severity  = "critical"
  metric    = "latency_p95"
  op        = ">"
  threshold = var.latency_ms
  window    = "5m"
  for       = "2m"
  channels  = [visionary_alert_channel.ops.id]
}

resource "visionary_alert_rule" "errors" {
  name             = "Server errors"
  metric           = "error_rate_5xx"
  op               = ">"
  threshold        = 2
  channels         = [visionary_alert_channel.ops.id]
  depends_on_rules = [visionary_alert_rule.slow.id]
}

resource "visionary_alert_silence" "nights" {
  name       = "Quiet nights"
  severities = ["info", "warning"]
  weekly = {
    days = [0, 1, 2, 3, 4, 5, 6]
    from = "22:00"
    to   = "07:00"
  }
}

resource "visionary_uptime_check" "site" {
  name       = "Website"
  url        = "https://example.com/"
  interval_s = 300
}

resource "visionary_dashboard" "api" {
  name = "API"
  panels = jsonencode([
    { id = "latency", kind = "builtin", ref = "overview/latency", x = 0, y = 0, w = 6, h = 4 },
    { id = "errors", kind = "builtin", ref = "overview/error_rate", x = 6, y = 0, w = 6, h = 4 },
  ])
}
