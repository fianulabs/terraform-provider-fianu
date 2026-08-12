# Look up an existing Fianu target by its path. The entity is read
# only — Terraform never creates, changes or archives it.

data "fianu_target" "existing" {
  path = "f.target.example"
}

output "target_uuid" {
  value = data.fianu_target.existing.uuid
}
