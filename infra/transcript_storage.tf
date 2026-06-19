# ============================================================================
# Session transcript blob storage
# ============================================================================
# Durable storage for SDK transcript JSONL snapshots so a session's
# conversation can be resurrected onto a fresh pod after pod death (the
# emptyDir-backed pod and its on-disk transcript are gone). The orchestrator
# writes whole-file snapshots through workload identity; the container is
# private and never served directly. See docs/session-transcript-capture.md.
# ============================================================================

resource "azurerm_storage_account" "transcripts" {
  name                     = "romainetanktranscripts"
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

resource "azurerm_storage_container" "transcripts" {
  name                  = "session-transcripts"
  storage_account_id    = azurerm_storage_account.transcripts.id
  container_access_type = "private"
}

resource "azurerm_role_assignment" "credential_refresher_transcripts" {
  scope                = azurerm_storage_container.transcripts.resource_manager_id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = azurerm_user_assigned_identity.credential_refresher.principal_id
}

output "transcripts_account_url" {
  value       = "https://${azurerm_storage_account.transcripts.name}.blob.core.windows.net"
  description = "Blob service URL for private Tank session transcripts."
}

output "transcripts_container" {
  value       = azurerm_storage_container.transcripts.name
  description = "Private blob container for Tank session transcripts."
}
