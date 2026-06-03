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
