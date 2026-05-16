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
    # Data-plane RBAC for romaine-kv. Subscription Contributor covers the
    # control plane but not secret reads/writes — secret-officer is what the
    # keyvault_get_secret / keyvault_set_secret tools call against.
    "romaine-kv-secrets-officer" = {
      scope                = data.azurerm_key_vault.main.id
      role_definition_name = "Key Vault Secrets Officer"
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

# ----------------------------------------------------------------------------
# mcp-tank-operator: thin shim — session CRUD on behalf of caller pod IP
# ----------------------------------------------------------------------------
# This server only calls the tank-operator orchestrator HTTP API
# (/api/internal/sessions/*) — no Azure surface, so no role_assignments.
# It does need a UAMI so the federated credential for GitHub Actions CI
# (docker build → ACR push) is wired through the standard module.

module "mcp_tank_operator" {
  source = "./mcp-server"

  name                     = "tank-operator"
  resource_group_name      = data.azurerm_resource_group.main.name
  resource_group_location  = data.azurerm_resource_group.main.location
  key_vault_id             = data.azurerm_key_vault.main.id
  aks_oidc_issuer_url      = local.aks_oidc_issuer_url
  aks_namespace            = "mcp-tank-operator"
  aks_service_account_name = "mcp-tank-operator"

  role_assignments = {}
}

# ----------------------------------------------------------------------------
# Per-server: auth
# ----------------------------------------------------------------------------
# Admin MCP for auth.romaine.life user management — list/promote/enroll
# users from a tank-operator session pod instead of clicking through the
# /admin console. Like mcp_tank_operator, this server's only outbound work
# is HTTP against auth.romaine.life's admin endpoints, so no Azure
# role_assignments. The UAMI exists so CI federation + the kv-published
# client ID land before the mcp-auth chart needs them.

module "mcp_auth" {
  source = "./mcp-server"

  name                     = "auth"
  resource_group_name      = data.azurerm_resource_group.main.name
  resource_group_location  = data.azurerm_resource_group.main.location
  key_vault_id             = data.azurerm_key_vault.main.id
  aks_oidc_issuer_url      = local.aks_oidc_issuer_url
  aks_namespace            = "mcp-auth"
  aks_service_account_name = "mcp-auth"

  role_assignments = {}
}
