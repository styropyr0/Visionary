# A place to run checks from: a VPS in Frankfurt running
#   VISIONARY_AGENT_LOCATION=frankfurt-vps VISIONARY_KEY=<key> visionary-agent
resource "visionary_uptime_location" "frankfurt" {
  name = "frankfurt-vps"
}

output "frankfurt_location_key" {
  value     = visionary_uptime_location.frankfurt.key
  sensitive = true
}

# Down only when most of its locations fail
resource "visionary_uptime_check" "shop" {
  name          = "Shop"
  url           = "https://shop.example.com"
  locations     = ["server", visionary_uptime_location.frankfurt.name]
  location_rule = "majority"
}
