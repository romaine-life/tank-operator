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
# consume the KV-published client IDs live in k8s-mcp-azure/ and
# k8s-mcp-github/.
# ============================================================================

# Tenant ID — not secret, but kept in KV so MCP ExternalSecrets can pull it
# alongside per-server IDs without anything having to know tenant specifics
# statically.
resource "azurerm_key_vault_secret" "mcp_tenant_id" {
  name         = "mcp-tenant-id"
  value        = data.azurerm_client_config.current.tenant_id
  key_vault_id = data.azurerm_key_vault.main.id
}

locals {
  mcp_azure_extra_reader_subscription_ids = setsubtract(
    toset([
      for id in split(",", var.mcp_azure_extra_reader_subscription_ids) :
      trimspace(id)
      if trimspace(id) != ""
    ]),
    [
      data.azurerm_client_config.current.subscription_id,
      local.aks_subscription_id,
    ],
  )
}

# ----------------------------------------------------------------------------
# Per-server: azure
# ----------------------------------------------------------------------------
# Hosts Microsoft's azure-mcp. The UAMI gets Reader at subscription scope for
# the primary infra subscription plus any explicit extra subscriptions. That
# keeps the MCP surface read-only; real infrastructure deployment still goes
# through repo Tofu workflows using their own CI identity.

module "mcp_azure" {
  source = "./mcp-server"

  name                     = "azure"
  resource_group_name      = data.azurerm_resource_group.main.name
  resource_group_location  = data.azurerm_resource_group.main.location
  key_vault_id             = data.azurerm_key_vault.main.id
  aks_oidc_issuer_url      = local.aks_oidc_issuer_url
  aks_namespace            = "mcp-azure"
  aks_service_account_name = "mcp-azure"

  role_assignments = merge(
    {
      "subscription-reader" = {
        scope                = "/subscriptions/${data.azurerm_client_config.current.subscription_id}"
        role_definition_name = "Reader"
      }
    },
    {
      for subscription_id in local.mcp_azure_extra_reader_subscription_ids :
      "extra-subscription-reader-${subscription_id}" => {
        scope                = "/subscriptions/${subscription_id}"
        role_definition_name = "Reader"
      }
    },
    {
      # KV data-plane access. Reader at the control plane gives us the
      # vault's metadata but NOT secret reads/writes — those need a data-plane
      # role. mcp-azure exposes both read and set-secret operations, so grant
      # secret management without broadening to keys/certs or RBAC admin.
      "kv-secrets-officer" = {
        scope                = data.azurerm_key_vault.main.id
        role_definition_name = "Key Vault Secrets Officer"
      }
    },
  )
}

# ----------------------------------------------------------------------------
# Per-server: azure-admin
# ----------------------------------------------------------------------------
# Small first-party MCP server with guarded destructive cleanup commands. The
# CI principal can assign built-in roles but cannot define custom roles, so the
# permission boundary is a separate UAMI plus exact-name confirmation in the
# tool implementation.

module "mcp_azure_admin" {
  source = "./mcp-server"

  name                     = "azure-admin"
  resource_group_name      = data.azurerm_resource_group.main.name
  resource_group_location  = data.azurerm_resource_group.main.location
  key_vault_id             = data.azurerm_key_vault.main.id
  aks_oidc_issuer_url      = local.aks_oidc_issuer_url
  aks_namespace            = "mcp-azure"
  aks_service_account_name = "mcp-azure-admin"

  role_assignments = {
    "subscription-cleanup-operator" = {
      scope                = "/subscriptions/${data.azurerm_client_config.current.subscription_id}"
      role_definition_name = "Contributor"
    }
  }
}

# ----------------------------------------------------------------------------
# mcp-azure: Cosmos data-plane reads
# ----------------------------------------------------------------------------
# Cosmos SQL API uses its own RBAC system (not ARM RBAC) — even Reader at
# subscription scope doesn't grant data-plane reads. Without a Cosmos-native
# role any cosmos query through azure-mcp comes back as 403 readMetadata.
#
# Account-scope Built-in Data Reader on every Cosmos account in this tenant
# so azure-mcp can inspect any database/container under them. Read-only;
# bump role_definition_id to `...000002` (Built-in Data Contributor) on a
# per-account basis if a write surface is later needed.

data "azurerm_cosmosdb_account" "infra_serverless" {
  name                = "infra-cosmos-serverless"
  resource_group_name = data.azurerm_resource_group.main.name
}

resource "azurerm_cosmosdb_sql_role_assignment" "mcp_azure_infra_serverless_reader" {
  resource_group_name = data.azurerm_resource_group.main.name
  account_name        = data.azurerm_cosmosdb_account.infra_serverless.name
  role_definition_id  = "${data.azurerm_cosmosdb_account.infra_serverless.id}/sqlRoleDefinitions/00000000-0000-0000-0000-000000000001"
  principal_id        = module.mcp_azure.managed_identity_principal_id
  scope               = data.azurerm_cosmosdb_account.infra_serverless.id
}

resource "azurerm_cosmosdb_sql_role_assignment" "mcp_azure_tank_operator_reader" {
  resource_group_name = data.azurerm_resource_group.main.name
  account_name        = azurerm_cosmosdb_account.tank_operator.name
  role_definition_id  = "${azurerm_cosmosdb_account.tank_operator.id}/sqlRoleDefinitions/00000000-0000-0000-0000-000000000001"
  principal_id        = module.mcp_azure.managed_identity_principal_id
  scope               = azurerm_cosmosdb_account.tank_operator.id
}
