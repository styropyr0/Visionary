# A channel made in the dashboard, used by a rule managed here
data "visionary_alert_channel" "oncall" {
  name = "On-call Slack"
}

resource "visionary_alert_rule" "down" {
  name      = "Instance down"
  metric    = "instance_silent"
  op        = ">"
  threshold = 5
  channels  = [data.visionary_alert_channel.oncall.id]
}
