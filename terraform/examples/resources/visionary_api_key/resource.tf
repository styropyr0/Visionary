# A server key for an SDK, handed to another module or a secret store.
resource "visionary_api_key" "checkout_sdk" {
  name  = "checkout service"
  scope = "server"
}

output "checkout_sdk_key" {
  value     = visionary_api_key.checkout_sdk.secret
  sensitive = true
}
