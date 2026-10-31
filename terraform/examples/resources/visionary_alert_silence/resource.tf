# Quiet hours: no info or warning notifications on weeknights
resource "visionary_alert_silence" "nights" {
  name       = "Quiet nights"
  severities = ["info", "warning"]

  weekly = {
    days = [1, 2, 3, 4, 5]
    from = "22:00"
    to   = "07:00"
    tz   = "Europe/Berlin"
  }
}

# A maintenance window for one rule
resource "visionary_alert_silence" "upgrade" {
  name     = "Database upgrade"
  rule_ids = [visionary_alert_rule.slow_checkout.id]

  once = {
    starts_at = "2026-12-05T22:00:00Z"
    ends_at   = "2026-12-06T02:00:00Z"
  }
}
