# p95 latency of the checkout route over 800 ms for 2 minutes.
resource "visionary_alert_rule" "slow_checkout" {
  name      = "Slow checkout"
  severity  = "critical"
  metric    = "latency_p95"
  op        = ">"
  threshold = 800
  window    = "5m"
  for       = "2m"
  channels  = [visionary_alert_channel.ops.id]
  runbook   = "https://wiki.example.com/runbooks/checkout"
  labels    = { team = "payments" }

  scope = {
    route = "/checkout"
  }

  # Nobody acknowledged it in 15 minutes: page.
  escalation = [{
    after    = "15m"
    channels = [visionary_alert_channel.pager.id]
  }]
}

# Traffic far from its usual level, in either direction.
resource "visionary_alert_rule" "traffic" {
  name        = "Unusual traffic"
  kind        = "anomaly"
  metric      = "requests_per_min"
  sensitivity = "medium"
  direction   = "both"
  channels    = [visionary_alert_channel.ops.id]
}

# The SLO burning its error budget too fast.
resource "visionary_alert_rule" "slo" {
  name     = "Checkout SLO"
  kind     = "slo"
  target   = 99.5
  channels = [visionary_alert_channel.ops.id]
}

# A PromQL expression.
resource "visionary_alert_rule" "queue" {
  name      = "Queue backing up"
  metric    = "promql"
  promql    = "sum(queue_depth)"
  op        = ">"
  threshold = 1000
}
