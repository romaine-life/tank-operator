# Shared Azure data sources used across this directory's tofu. Resources
# themselves (RG, ACR, AKS, KV) live in infra-bootstrap; tank-operator only
# reads them.

data "azuread_client_config" "current" {}

data "azurerm_client_config" "current" {}

data "azurerm_resource_group" "main" {
  name = "infra"
}

data "azurerm_key_vault" "main" {
  name                = var.key_vault_name
  resource_group_name = var.key_vault_resource_group
}

data "azurerm_container_registry" "main" {
  name                = "romainecr"
  resource_group_name = data.azurerm_resource_group.main.name
}

data "terraform_remote_state" "infra_bootstrap" {
  backend = "azurerm"

  config = {
    resource_group_name  = "infra"
    storage_account_name = "nelsontofu"
    container_name       = "tfstate"
    key                  = "infra-bootstrap.tfstate"
    use_oidc             = true
  }
}

locals {
  aks_cluster_id        = data.terraform_remote_state.infra_bootstrap.outputs.aks_cluster_id
  aks_oidc_issuer_url   = data.terraform_remote_state.infra_bootstrap.outputs.aks_oidc_issuer_url
  aks_subscription_id   = split("/", local.aks_cluster_id)[2]
}
