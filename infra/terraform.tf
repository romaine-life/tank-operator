terraform {
  # required_version + required_providers come from shared-providers.tf,
  # which the tofu-plan-apply-template workflow overlays into this dir
  # from nelsong6/infra-bootstrap/tofu/provider/. Don't pin providers
  # locally — the org-wide intent is single-source provider versions.
  #
  # resource_group_name / storage_account_name / container_name / key
  # for the backend are passed by the workflow via `-backend-config=`.
  backend "azurerm" {
    use_oidc = true
  }
}
