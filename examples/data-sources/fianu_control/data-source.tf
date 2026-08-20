# Look up an existing Fianu control by its path. The entity is read
# only — Terraform never creates, changes or archives it.

data "fianu_control" "existing" {
  path = "f.control.example"
}

output "control_uuid" {
  value = data.fianu_control.existing.uuid
}
