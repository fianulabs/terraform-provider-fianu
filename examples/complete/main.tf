terraform {
  required_version = ">= 1.12.0"
  required_providers {
    fianu = {
      source  = "fianulabs/fianu"
      version = "0.1.0"
    }
  }
}

provider "fianu" {
  host = var.fianu_host
  # OIDC client-credentials. Falls back to FIANU_CLIENT_ID / FIANU_CLIENT_SECRET
  # / FIANU_TOKEN_URL env vars when these are unset.
  client_id     = var.fianu_client_id
  client_secret = var.fianu_client_secret
  token_url     = var.fianu_token_url
}

variable "fianu_host" {
  type    = string
  default = "https://console.fianu.io"
}

variable "fianu_client_id" {
  type      = string
  sensitive = true
}

variable "fianu_client_secret" {
  type      = string
  sensitive = true
}

variable "fianu_token_url" {
  type    = string
  default = "https://auth.fianu.io/oauth/token"
}

resource "fianu_control" "payment_service_sast" {
  path = "payment-service-sast"
  name = "Payment Service SAST"

  detail = {
    full_name   = "Payment Service Static Analysis"
    display_key = "PSSAST"
    description = "Static analysis gates for the payment service repository."
  }
}

output "control_id" {
  description = "Composite Terraform resource ID."
  value       = fianu_control.payment_service_sast.id
}

output "control_uuid" {
  description = "Server-generated UUID stable across versions."
  value       = fianu_control.payment_service_sast.uuid
}
