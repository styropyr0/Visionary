resource "visionary_alert_channel" "ops" {
  name = "Ops email"
  kind = "email"
  to   = ["ops@example.com"]

  digest_enabled = true
  digest_weekday = 1 # Monday
  digest_hour    = 9
}

resource "visionary_alert_channel" "slack" {
  name = "#alerts"
  kind = "slack"
  url  = var.slack_webhook_url
}

resource "visionary_alert_channel" "pager" {
  name   = "PagerDuty"
  kind   = "pagerduty"
  secret = var.pagerduty_routing_key
}

variable "slack_webhook_url" {
  type      = string
  sensitive = true
}

variable "pagerduty_routing_key" {
  type      = string
  sensitive = true
}
