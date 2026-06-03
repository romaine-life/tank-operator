provider "azurerm" {
  features {}
  use_oidc = true
  # subscription_id / tenant_id come from the ARM_* env vars the shared
  # tofu workflow exports for OIDC auth — no need to plumb them through
  # tofu variables.
  resource_provider_registrations = "none"
}

provider "azuread" {
  use_oidc = true
  # tenant_id likewise comes from ARM_TENANT_ID env.
}

provider "random" {}
