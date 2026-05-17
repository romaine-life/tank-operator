variable "github_pat" {
  description = "GitHub PAT for the github provider. Fetched from KV by the tofu workflow's secrets job and passed via tofu_vars -var=github_pat=… into the shared plan/apply template."
  type        = string
  sensitive   = true
}

variable "key_vault_name" {
  description = "Name of the Key Vault that stores the OAuth client secret + cookie secret."
  type        = string
  default     = "romaine-kv"
}

variable "key_vault_resource_group" {
  description = "Resource group containing key_vault_name."
  type        = string
  default     = "infra"
}
