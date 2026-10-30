resource "visionary_dashboard" "checkout" {
  name        = "Checkout"
  description = "Latency, errors and traffic of the checkout service"

  # Built-in charts by ref (overview/…, performance/…, system/…), or a saved
  # panel by its id with kind = "saved". The grid is 12 columns wide.
  panels = jsonencode([
    { id = "latency", kind = "builtin", ref = "overview/latency", x = 0, y = 0, w = 6, h = 4 },
    { id = "errors", kind = "builtin", ref = "overview/error_rate", x = 6, y = 0, w = 6, h = 4 },
    { id = "traffic", kind = "builtin", ref = "overview/traffic", x = 0, y = 4, w = 12, h = 4, title = "Requests" },
  ])

  variables = [{ name = "service", default = "checkout" }]
  time      = { kind = "relative", key = "24h" }
}
