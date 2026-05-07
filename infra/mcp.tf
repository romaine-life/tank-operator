# ============================================================================
# MCP (Model Context Protocol) Servers
# ============================================================================
# Each MCP server runs in-cluster as its own pod with its own dedicated
# managed identity. Clients (session pods) authenticate to the server using
# their projected K8s SA token; a kube-rbac-proxy sidecar in the chart
# performs the TokenReview. Upstream Azure permissions live on the server's
# UAMI — anyone authenticated to the server inherits them, by design.
#
# Per-server resources (UAMI, federated credential, role assignments,
# KV-published client ID) live in the ./mcp-server module. Helm charts that
# consume the KV-published client IDs live in the MCP server chart repos.
# ============================================================================

# Tenant ID — not secret, but kept in KV so MCP ExternalSecrets can pull it
# alongside per-server IDs without anything having to know tenant specifics
# statically.
resource "azurerm_key_vault_secret" "mcp_tenant_id" {
  name         = "mcp-tenant-id"
  value        = data.azurerm_client_config.current.tenant_id
  key_vault_id = data.azurerm_key_vault.main.id
}

# ----------------------------------------------------------------------------
# Per-server: azure-personal
# ----------------------------------------------------------------------------
# First-party Azure MCP server for personal operational tools. The permission
# boundary is a separate UAMI plus intentionally scoped MCP tools: discovery is
# read-only, and destructive operations require exact-name confirmations.

module "mcp_azure_personal" {
  source = "./mcp-server"

  name                     = "azure-personal"
  resource_group_name      = data.azurerm_resource_group.main.name
  resource_group_location  = data.azurerm_resource_group.main.location
  key_vault_id             = data.azurerm_key_vault.main.id
  aks_oidc_issuer_url      = local.aks_oidc_issuer_url
  aks_namespace            = "mcp-azure"
  aks_service_account_name = "mcp-azure-personal"

  role_assignments = {
    "subscription-operator" = {
      scope                = "/subscriptions/${data.azurerm_client_config.current.subscription_id}"
      role_definition_name = "Contributor"
    }
  }
}

# ----------------------------------------------------------------------------
# azure-personal: Cosmos data-plane access
# ----------------------------------------------------------------------------
# Cosmos SQL API uses its own RBAC system (not ARM RBAC) — even Reader at
# subscription scope doesn't grant data-plane reads or writes. The personal
# server exposes guarded Cosmos write tools with dry-run defaults, so grant
# account-scope Built-in Data Contributor on the Cosmos accounts it needs.

data "azurerm_cosmosdb_account" "infra_serverless" {
  name                = "infra-cosmos-serverless"
  resource_group_name = data.azurerm_resource_group.main.name
}

resource "azurerm_cosmosdb_sql_role_assignment" "mcp_azure_personal_infra_serverless_contributor" {
  resource_group_name = data.azurerm_resource_group.main.name
  account_name        = data.azurerm_cosmosdb_account.infra_serverless.name
  role_definition_id  = "${data.azurerm_cosmosdb_account.infra_serverless.id}/sqlRoleDefinitions/00000000-0000-0000-0000-000000000002"
  principal_id        = module.mcp_azure_personal.managed_identity_principal_id
  scope               = data.azurerm_cosmosdb_account.infra_serverless.id
}

resource "azurerm_cosmosdb_sql_role_assignment" "mcp_azure_personal_tank_operator_contributor" {
  resource_group_name = data.azurerm_resource_group.main.name
  account_name        = azurerm_cosmosdb_account.tank_operator.name
  role_definition_id  = "${azurerm_cosmosdb_account.tank_operator.id}/sqlRoleDefinitions/00000000-0000-0000-0000-000000000002"
  principal_id        = module.mcp_azure_personal.managed_identity_principal_id
  scope               = azurerm_cosmosdb_account.tank_operator.id
}

# Glimmung reconciles dynamic validation-slot workload identity subjects via
# the azure-personal MCP tool. Keep the permission boundary in Tank: the MCP
# identity may manage federated credentials only on the two Tank UAMIs that
# validation slots need to use.
resource "azurerm_role_assignment" "mcp_azure_personal_slot_federation" {
  for_each = {
    credentials = azurerm_user_assigned_identity.credential_refresher.id
    api_proxy   = azurerm_user_assigned_identity.api_proxy.id
  }

  scope                = each.value
  role_definition_name = "Managed Identity Contributor"
  principal_id         = module.mcp_azure_personal.managed_identity_principal_id
  principal_type       = "ServicePrincipal"
}
