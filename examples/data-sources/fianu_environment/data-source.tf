# Look up an existing Fianu environment by its path. The entity is read
# only — Terraform never creates, changes or archives it.

data "fianu_environment" "existing" {
  path = "f.environment.example"
}

output "environment_uuid" {
  value = data.fianu_environment.existing.uuid
}
