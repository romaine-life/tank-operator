# ============================================================================
# Azure Database for PostgreSQL — Flexible Server
# ============================================================================
# Replaces the tank-operator-romaine Cosmos account (profiles + session-events
# ledger). Postgres lets us match the access patterns the inspirations doc
# expects of a durable history store — indexed queries, real backups via PITR,
# point-in-time recovery — without paying Cosmos's per-RU write costs.
#
# Sized for hobby/portfolio scale: B2s (2 vCores burstable, 4 GiB RAM), single
# AZ, no HA tier. ~$30/mo flat vs ~$73/mo Cosmos serverless at current write
# volume.
#
# Auth: both password and AAD enabled at the server level. The orchestrator
# pod uses AAD only (workload-identity token exchange for the DB). The
# password is for break-glass admin access and lives in Key Vault. Disabling
# password auth entirely is a tightening step for a follow-up once AAD-only
# operations are confirmed.
# ============================================================================

resource "random_password" "pg_admin" {
  length      = 32
  special     = true
  min_lower   = 1
  min_upper   = 1
  min_numeric = 1
  min_special = 1
  # The Postgres admin login forbids these characters in passwords.
  override_special = "!#$%&*+-_=?"
}

resource "azurerm_postgresql_flexible_server" "tank_operator" {
  name                = "tank-operator-db"
  resource_group_name = data.azurerm_resource_group.main.name

  # Pinned to westus3 because the subscription's westus2 capacity for
  # Flexible Server is currently restricted (`LocationIsOfferRestricted`).
  # westus3 is in the same physical area as westus2; latency from AKS in
  # westus2 to this DB is comparable to intra-region and egress cost at the
  # current write volume is sub-dollar. Move back to westus2 if/when the
  # quota request lands (https://aka.ms/postgres-request-quota-increase).
  location = "westus3"

  version    = "16"
  sku_name   = "B_Standard_B2s"
  storage_mb = 32768
  zone       = "1"

  # Public endpoint, gated by AAD auth at the data plane and the
  # Azure-internal firewall rule below. VNet integration is a later
  # tightening if private-only access becomes a requirement; for now this
  # matches how `infra-cosmos-serverless` is exposed.
  public_network_access_enabled = true

  authentication {
    active_directory_auth_enabled = true
    password_auth_enabled         = true
    tenant_id                     = data.azurerm_client_config.current.tenant_id
  }

  administrator_login    = "pgadmin"
  administrator_password = random_password.pg_admin.result

  backup_retention_days        = 7
  geo_redundant_backup_enabled = false

  lifecycle {
    ignore_changes = [
      # AZ can be reassigned during planned maintenance; don't fight it.
      zone,
    ]
  }
}

# AAD admin — the orchestrator's UAMI. Granting it administrator rather than a
# narrower Postgres role keeps the wiring simple: the same identity that
# already federates from the orchestrator pod becomes the DB admin, and any
# schema migration the app runs at startup happens under that identity. If we
# later want non-admin app roles, they get created via SQL by this admin.
resource "azurerm_postgresql_flexible_server_active_directory_administrator" "orchestrator" {
  server_name         = azurerm_postgresql_flexible_server.tank_operator.name
  resource_group_name = data.azurerm_resource_group.main.name
  tenant_id           = data.azurerm_client_config.current.tenant_id
  object_id           = azurerm_user_assigned_identity.credential_refresher.principal_id
  principal_name      = azurerm_user_assigned_identity.credential_refresher.name
  principal_type      = "ServicePrincipal"
}

resource "azurerm_postgresql_flexible_server_database" "tank_operator" {
  name      = "tank-operator"
  server_id = azurerm_postgresql_flexible_server.tank_operator.id
  collation = "en_US.utf8"
  charset   = "utf8"
}

# Firewall: allow Azure-internal traffic. The 0.0.0.0/0.0.0.0 magic rule is
# the Flexible-Server equivalent of Cosmos's "allow Azure services" — it
# whitelists traffic from any Azure resource in any subscription, gated by
# AAD auth at the data plane. AKS outbound flows through the standard LB and
# reaches this server as Azure-internal.
resource "azurerm_postgresql_flexible_server_firewall_rule" "allow_azure_internal" {
  name             = "allow-azure-internal"
  server_id        = azurerm_postgresql_flexible_server.tank_operator.id
  start_ip_address = "0.0.0.0"
  end_ip_address   = "0.0.0.0"
}

# Publish the server FQDN so the orchestrator's Helm chart can read it as an
# ExternalSecret or env var without remote-state plumbing in the app repo.
resource "azurerm_key_vault_secret" "postgres_host" {
  name         = "tank-operator-pg-host"
  value        = azurerm_postgresql_flexible_server.tank_operator.fqdn
  key_vault_id = data.azurerm_key_vault.main.id
}

# Break-glass admin password. The orchestrator should never read this; it
# authenticates via AAD. This is for human ops only — connect with
# `psql "host=<fqdn> user=pgadmin dbname=tank-operator sslmode=require"`
# and PGPASSWORD set to this value.
resource "azurerm_key_vault_secret" "postgres_admin_password" {
  name         = "tank-operator-pg-admin-password"
  value        = random_password.pg_admin.result
  key_vault_id = data.azurerm_key_vault.main.id
}

resource "azurerm_postgresql_flexible_server_configuration" "max_connections" {
  name      = "max_connections"
  server_id = azurerm_postgresql_flexible_server.tank_operator.id
  value     = "100"
}

output "postgres_fqdn" {
  value       = azurerm_postgresql_flexible_server.tank_operator.fqdn
  description = "FQDN of the tank-operator Postgres Flexible Server."
}

output "postgres_database_name" {
  value       = azurerm_postgresql_flexible_server_database.tank_operator.name
  description = "Name of the application database inside the Flexible Server."
}
