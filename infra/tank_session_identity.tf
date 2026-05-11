# ============================================================================
# Session-pod UAMI — pod-side workload identity for Cosmos
# ============================================================================
# Identity the session pods (claude-session SA) use to write run events to
# Cosmos from the Phase B pod-side agent-runner. Decoupled from the
# orchestrator's `claude-credentials-refresher-identity` and the api-proxy's
# `claude-api-proxy-identity` on the same least-privilege grounds those two
# are decoupled from each other: each identity has only the Azure surface
# its workload actually needs.
#
# Pre-Phase B the UAMI exists but no pod consumes it (Phase A ships infra
# inert; Phase B's Node runner is what reads the env). Pre-creating it
# keeps the Tofu and chart sides independent — chart can land first and
# wait for an apply, or vice versa.
# ============================================================================

resource "azurerm_user_assigned_identity" "tank_session" {
  name                = "tank-session-identity"
  resource_group_name = data.azurerm_resource_group.main.name
  location            = data.azurerm_resource_group.main.location
}

# Federation against the session SA. Subject is fixed because the SA name
# and namespace are stable across Helm releases (see
# `tank-operator.sessionServiceAccount` and `tank-operator.sessionsNamespace`
# helpers in k8s/templates/_helpers.tpl).
resource "azurerm_federated_identity_credential" "tank_session" {
  name                = "aks-tank-session"
  resource_group_name = data.azurerm_resource_group.main.name
  parent_id           = azurerm_user_assigned_identity.tank_session.id
  audience            = ["api://AzureADTokenExchange"]
  issuer              = local.aks_oidc_issuer_url
  subject             = "system:serviceaccount:tank-operator-sessions:claude-session"
}

# Cosmos Built-in Data Contributor scoped to the tank-operator account.
# Same role the orchestrator's UAMI has on this account — sessions write
# run events (and read them on session-open for reconnect history),
# nothing else. Account-scoped rather than per-container because Cosmos
# SQL role assignments aren't currently per-container-scopable in the
# Tofu provider and the blast radius is the same database either way.
resource "azurerm_cosmosdb_sql_role_assignment" "tank_session_cosmos" {
  resource_group_name = data.azurerm_resource_group.main.name
  account_name        = azurerm_cosmosdb_account.tank_operator.name
  role_definition_id  = "${azurerm_cosmosdb_account.tank_operator.id}/sqlRoleDefinitions/00000000-0000-0000-0000-000000000002"
  principal_id        = azurerm_user_assigned_identity.tank_session.principal_id
  scope               = azurerm_cosmosdb_account.tank_operator.id
}

# Publish the UAMI's client_id to KV so the sessions-namespace
# ExternalSecret can sync it into AZURE_CLIENT_ID. Tenant ID is shared
# with the rest of the cluster via the existing `mcp-tenant-id` KV
# secret (infra/mcp.tf), so we don't republish it here.
resource "azurerm_key_vault_secret" "tank_session_client_id" {
  name         = "tank-session-mi-client-id"
  value        = azurerm_user_assigned_identity.tank_session.client_id
  key_vault_id = data.azurerm_key_vault.main.id
}
