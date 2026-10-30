terraform {
  required_providers {
    visionary = {
      source = "styropyr0/visionary"
    }
  }
}

# An editor key manages alerts, checks and dashboards; an admin key also
# manages API keys. Create one in Settings → API keys.
provider "visionary" {
  url = "https://visionary.example.com"
  # api_key = "vis_..."  # or set VISIONARY_API_KEY
}
