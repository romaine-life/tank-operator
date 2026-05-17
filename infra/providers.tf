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

# Repo-scoped Actions variables require admin perms that the default
# GITHUB_TOKEN doesn't have. Use the same `github-pat` PAT in KV that
# infra-bootstrap uses; the tofu workflow fetches it in a preliminary
# job, masks it, and passes it through `tofu_vars` as -var=github_pat=…
provider "github" {
  owner = "nelsong6"
  token = var.github_pat
}
