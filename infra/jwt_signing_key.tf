# ============================================================================
# Tank-operator session-JWT signing key — Key Vault Keys API
# ============================================================================
# RSA key used to sign the orchestrator's session and install-state JWTs.
# Lives in Key Vault as a Key (not a Secret) so the private bytes never leave
# the vault — the orchestrator pod calls KV's sign operation per token mint
# (a few times per user per week), and verifies in-process using the cached
# public key. Trades a one-time ~50ms KV roundtrip on login for the property
# that a compromised orchestrator pod can verify but cannot forge tokens.
#
# Replaces the prior HS256 shared-secret design (random_password →
# azurerm_key_vault_secret.jwt_secret) where the secret bytes lived in the
# pod's env and any process that could exec into tank-operator could mint a
# JWT for any allowlisted email.
#
# Rotation: bump curve_or_size or run `az keyvault key rotate`; KV creates a
# new version while old versions still verify. The minter stamps `kid` (= key
# version) in the JWT header so the verifier can resolve the right public key
# per token during rollover.
#
# Hard cutover from HS256: the existing tank-operator-jwt-secret KV secret
# remains in place during the deploy of the verifier-switch PR so any
# orchestrator pods still on the prior image keep working until they roll;
# a follow-up will remove that resource once the rollout is settled.
# ============================================================================

resource "azurerm_key_vault_key" "tank_operator_jwt" {
  name         = "tank-operator-jwt-signing"
  key_vault_id = data.azurerm_key_vault.main.id
  key_type     = "RSA"
  key_size     = 2048

  # The minimum set the orchestrator needs: sign with the private key in KV,
  # publish the public key for in-process verification. No encrypt/wrap/etc.
  key_opts = [
    "sign",
    "verify",
  ]
}

# `Key Vault Crypto User` is the narrowest built-in role that grants both
# `Microsoft.KeyVault/vaults/keys/read` (needed to fetch the public key for
# verification) and the data-plane `sign`/`verify` operations. The next role
# up (`Crypto Officer`) would also allow key creation and deletion — that
# belongs to the infra deployment identity, not the orchestrator pod.
resource "azurerm_role_assignment" "orchestrator_jwt_crypto_user" {
  scope                = azurerm_key_vault_key.tank_operator_jwt.resource_versionless_id
  role_definition_name = "Key Vault Crypto User"
  principal_id         = azurerm_user_assigned_identity.credential_refresher.principal_id
}
