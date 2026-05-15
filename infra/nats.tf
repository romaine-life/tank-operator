# NATS JetStream auth token. The Helm chart mirrors this through
# ExternalSecrets into both the orchestrator and session namespaces.
resource "random_password" "nats_token" {
  length  = 48
  special = false
}

resource "azurerm_key_vault_secret" "nats_token" {
  name         = "tank-nats-token"
  value        = random_password.nats_token.result
  key_vault_id = data.azurerm_key_vault.main.id
}
