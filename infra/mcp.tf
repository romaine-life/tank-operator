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
# deleting them in Azure (lifecycle.destroy = false), after which the import
# on the MCP repo's side adopts them. See that repo's infra/README.md for
# the runbook.

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
# Deleting the resources from config rather than using
# `removed { destroy = false }` — the FIC subject on the existing Azure
# resources is stale (built when `aks_namespace = "mcp-azure"`, never
# updated after the chart-side namespace rename in
# nelsong6/mcp-azure-personal#12), so workload identity is broken anyway.
# A clean destroy-recreate via the companion mcp-azure-personal PR
# lets the new state start fresh with the correct
# `aks_namespace = "mcp-azure-personal"` and the correct FIC subject.
#
# Apply order with the companion PR:
#   1. Merge this PR. CI applies; tofu destroys the UAMI, FIC, role
#      assignments, KV secret, and Cosmos data-plane role assignment in
#      Azure. MCP server stops authenticating (already broken — no impact).
#   2. Merge nelsong6/mcp-azure-personal#10. Its CI applies; tofu creates
#      the same resources fresh with the correct namespace, restoring
#      the MCP server.
