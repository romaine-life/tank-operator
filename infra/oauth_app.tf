# Entra app reg for the tank-operator web UI sign-in flow.
#
# Public SPA + the kill-me/microsoft-routes pattern: the browser uses MSAL.js
# to obtain an Entra ID token, POSTs it to /api/auth/microsoft/login, the
# backend validates it via JWKS and mints its own short-lived JWT for the
# remainder of the session. No confidential client secret needed.
#
# Distinct from the tank-operator CI Entra app (which is for tofu/ACR push
# from GitHub Actions). The CI app's SP creates this one and becomes its
# owner; without the explicit `owners` attribute, the azuread provider
# doesn't record ownership and `Application.ReadWrite.OwnedBy` returns 403
# on any follow-up Graph call.

resource "azuread_application" "oauth" {
  display_name = "tank-operator-oauth"
  # Personal MSA accounts (e.g. outlook.com) need this; AzureADMyOrg-only apps
  # rejected by the consumer auth flow with `unauthorized_client`. Sign-in is
  # still gated by the backend's ALLOWED_EMAILS allowlist.
  sign_in_audience = "AzureADandPersonalMicrosoftAccount"
  # The Electron shell uses a native public-client auth-code flow through the
  # system browser, then receives the result on tank-operator://auth.
  fallback_public_client_enabled = true

  owners = [data.azuread_client_config.current.object_id]

  # v2 access tokens are required when sign_in_audience includes
  # PersonalMicrosoftAccount; absent this block, tofu refuses to plan.
  api {
    requested_access_token_version = 2
  }

  # SPA platform — MSAL.js auth-code-with-PKCE flow, no client secret.
  single_page_application {
    redirect_uris = [
      "https://${var.hostname}/",
    ]
  }

  public_client {
    redirect_uris = [
      "tank-operator://auth",
    ]
  }

  # Microsoft Graph: User.Read (delegated) is enough for MSAL to fetch the
  # signed-in user's profile (email, name) for the ID token claims.
  required_resource_access {
    resource_app_id = "00000003-0000-0000-c000-000000000000"

    resource_access {
      id   = "e1fe6dd8-ba31-4d61-89e7-88639da4683d" # User.Read
      type = "Scope"
    }
  }
}

resource "azuread_application" "oauth_test" {
  display_name = "tank-operator-oauth-test"
  # Same public-client posture as prod, but this app is for native-webapp
  # validation slots. Glimmung owns the slot redirect URI list because it
  # owns standby DNS/count reconciliation.
  sign_in_audience               = "AzureADandPersonalMicrosoftAccount"
  fallback_public_client_enabled = true
  owners = [
    data.azuread_client_config.current.object_id,
    module.mcp_azure_personal.managed_identity_principal_id,
  ]

  api {
    requested_access_token_version = 2
  }

  single_page_application {
    # Bootstrap the current standby set so the test app is usable immediately.
    # Glimmung owns changes after creation; ignore_changes below prevents Tofu
    # from fighting slot-count reconciliation.
    redirect_uris = [
      "https://tank-slot-1.tank.dev.romaine.life/",
      "https://tank-slot-2.tank.dev.romaine.life/",
      "https://tank-slot-3.tank.dev.romaine.life/",
    ]
  }

  lifecycle {
    ignore_changes = [
      single_page_application[0].redirect_uris,
    ]
  }

  required_resource_access {
    resource_app_id = "00000003-0000-0000-c000-000000000000"

    resource_access {
      id   = "e1fe6dd8-ba31-4d61-89e7-88639da4683d" # User.Read
      type = "Scope"
    }
  }
}

resource "azuread_service_principal" "oauth" {
  client_id = azuread_application.oauth.client_id
}

resource "azuread_service_principal" "oauth_test" {
  client_id = azuread_application.oauth_test.client_id
}

# Self-signed JWT secret used by the backend to mint per-session tokens after
# verifying the Entra ID token. Single secret, rotate by tainting and applying;
# tainting invalidates all live sessions, which is fine for a single-user tool.
resource "random_password" "jwt_secret" {
  length  = 64
  special = false
}

resource "azurerm_key_vault_secret" "oauth_client_id" {
  name         = "tank-operator-oauth-client-id"
  value        = azuread_application.oauth.client_id
  key_vault_id = data.azurerm_key_vault.main.id
}

resource "azurerm_key_vault_secret" "oauth_test_client_id" {
  name         = "tank-operator-test-oauth-client-id"
  value        = azuread_application.oauth_test.client_id
  key_vault_id = data.azurerm_key_vault.main.id
}

resource "azurerm_key_vault_secret" "jwt_secret" {
  name         = "tank-operator-jwt-secret"
  value        = random_password.jwt_secret.result
  key_vault_id = data.azurerm_key_vault.main.id
}

# Comma-joined list — the backend splits on `,` and lowercases on startup.
# KV secrets are flat strings, so this is the simplest stable encoding.
resource "azurerm_key_vault_secret" "oauth_allowed_emails" {
  name         = "tank-operator-oauth-allowed-emails"
  value        = join(",", var.allowed_emails)
  key_vault_id = data.azurerm_key_vault.main.id
}

# Allow the azure-personal MCP UAMI to manage redirect URIs on app
# registrations it explicitly owns, without granting broad directory list
# permissions. The test OAuth app grants ownership above; production remains
# owned only by the infra deployment identity.
data "azuread_service_principal" "microsoft_graph" {
  client_id = "00000003-0000-0000-c000-000000000000"
}

resource "azuread_app_role_assignment" "mcp_azure_personal_application_readwrite_ownedby" {
  app_role_id         = data.azuread_service_principal.microsoft_graph.app_role_ids["Application.ReadWrite.OwnedBy"]
  principal_object_id = module.mcp_azure_personal.managed_identity_principal_id
  resource_object_id  = data.azuread_service_principal.microsoft_graph.object_id
}
