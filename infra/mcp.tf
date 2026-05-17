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
# Migrated out: this server's UAMI + role assignments + Cosmos data-plane
# grant now live in nelsong6/mcp-azure-personal/infra/. The `removed` blocks
# at the bottom of this file forget the resources from this state without
# deleting them in Azure, after which the import on the MCP repo's side
# adopts them. See that repo's infra/README.md for the runbook.

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

# ============================================================================
# Migration: mcp_azure_personal moved to nelsong6/mcp-azure-personal/infra/
# ============================================================================
# State for the moved resources is removed manually with `tofu state rm`
# rather than via `removed { lifecycle.destroy = false }` blocks: OpenTofu
# 1.9 (this repo's pinned version) treats a bare `removed` block as
# DESTROY-then-remove, and the `lifecycle.destroy = false` opt-out only
# lands in 1.10. The MCP repo's apply imports the existing Azure resources
# into its state first; this side then runs the documented state-rm
# commands; nothing in Azure gets touched.
#
# Runbook (run from infra/ after `tofu init`):
#
#   tofu state rm module.mcp_azure_personal
#   tofu state rm azurerm_cosmosdb_sql_role_assignment.mcp_azure_personal_infra_serverless_contributor
#
# Then merge this PR and let CI apply; the plan should show no changes.
