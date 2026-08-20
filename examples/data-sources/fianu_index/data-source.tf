# Look up an existing Fianu index by its path. The entity is read
# only — Terraform never creates, changes or archives it.

data "fianu_index" "existing" {
  path = "f.index.example"
}

output "index_uuid" {
  value = data.fianu_index.existing.uuid
}
