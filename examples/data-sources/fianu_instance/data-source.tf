# Look up an existing Fianu instance by its path. The entity is read
# only — Terraform never creates, changes or archives it.

data "fianu_instance" "existing" {
  path = "f.instance.example"
}

output "instance_uuid" {
  value = data.fianu_instance.existing.uuid
}
