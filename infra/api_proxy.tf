# ============================================================================
# Anthropic API proxy — Azure side
# ============================================================================
# UAMI for the Envoy + ext_proc sidecars that front provider APIs
# (api.anthropic.com and chatgpt.com). The proxies read OAuth blobs from
# orchestrator-namespace Secrets (mirrored by ESO) and write rotated blobs
# back to KV after successful refresh.
#
# Separate from `claude-credentials-refresher-identity` (the orchestrator's
# UAMI) on least-privilege grounds: the api-proxy doesn't need any K8s
# API permissions, just KV get + set on the one secret. Decoupling SAs
# makes a future scale-out of the proxy (multiple replicas with their
# own SA) trivially safe.
# ============================================================================

resource "azurerm_user_assigned_identity" "api_proxy" {
  name                = "claude-api-proxy-identity"
  resource_group_name = data.azurerm_resource_group.main.name
  location            = data.azurerm_resource_group.main.location
}

resource "azurerm_federated_identity_credential" "api_proxy" {
  name                = "aks-claude-api-proxy"
  resource_group_name = data.azurerm_resource_group.main.name
  parent_id           = azurerm_user_assigned_identity.api_proxy.id
  audience            = ["api://AzureADTokenExchange"]
  issuer              = local.aks_oidc_issuer_url
  subject             = "system:serviceaccount:tank-operator:claude-api-proxy"
}

# Same justification as credential_refresher_kv: get+set on the credentials
# secret is the entire Azure surface this identity uses, vault scope is the
# narrowest built-in role we can pick without hand-rolling a custom one.
resource "azurerm_role_assignment" "api_proxy_kv" {
  scope                = data.azurerm_key_vault.main.id
  role_definition_name = "Key Vault Secrets Officer"
  principal_id         = azurerm_user_assigned_identity.api_proxy.principal_id
}

resource "azurerm_key_vault_secret" "api_proxy_client_id" {
  name         = "claude-api-proxy-mi-client-id"
  value        = azurerm_user_assigned_identity.api_proxy.client_id
  key_vault_id = data.azurerm_key_vault.main.id
}

# The antigravity-api-proxy reuses this same UAMI (vault-wide Secrets Officer),
# so it can read/write `antigravity-credentials` and read the Google OAuth
# installed-app client_secret. That client_secret lives in KV under
# `antigravity-oauth-client-secret` and is seeded OUT-OF-BAND (not declared
# here), exactly like the provider credential blobs (`claude-code-credentials`,
# `codex-credentials`, `antigravity-credentials`) which the login wizard mints.
# The value is extractable from the public agy binary via `strings`, so it is
# not confidential, but keeping it out of TF state matches the credential
# convention. The api-proxy config ExternalSecret syncs it into the
# ANTIGRAVITY_CLIENT_SECRET env (see k8s/templates/api-proxy.yaml).
