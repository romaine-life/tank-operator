# ============================================================================
# Avatar asset blob storage
# ============================================================================
# The avatar admin console stores metadata in Postgres and image bytes in this
# private blob container. The orchestrator brokers all reads through
# authenticated API routes; the container itself is not public.
# ============================================================================

resource "azurerm_storage_account" "avatar_assets" {
  name                     = "romainetankavatars"
  resource_group_name      = data.azurerm_resource_group.main.name
  location                 = data.azurerm_resource_group.main.location
  account_tier             = "Standard"
  account_replication_type = "LRS"

  allow_nested_items_to_be_public = false
  min_tls_version                 = "TLS1_2"

  blob_properties {
    delete_retention_policy {
      days = 30
    }

    container_delete_retention_policy {
      days = 30
    }
  }
}

resource "azurerm_storage_container" "avatar_assets" {
  name                  = "avatar-assets"
  storage_account_id    = azurerm_storage_account.avatar_assets.id
  container_access_type = "private"
}

resource "azurerm_role_assignment" "credential_refresher_avatar_assets" {
  scope                = azurerm_storage_container.avatar_assets.resource_manager_id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = azurerm_user_assigned_identity.credential_refresher.principal_id
}

output "avatar_assets_account_url" {
  value       = "https://${azurerm_storage_account.avatar_assets.name}.blob.core.windows.net"
  description = "Blob service URL for private Tank avatar assets."
}

output "avatar_assets_container" {
  value       = azurerm_storage_container.avatar_assets.name
  description = "Private blob container for Tank avatar assets."
}
