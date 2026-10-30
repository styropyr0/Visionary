# One error-rate rule per service that reports to Visionary.
data "visionary_services" "all" {}

resource "visionary_alert_rule" "errors" {
  for_each = toset(data.visionary_services.all.services)

  name      = "Errors in ${each.key}"
  metric    = "error_rate_5xx"
  op        = ">"
  threshold = 5
  scope     = { service = each.key }
}
