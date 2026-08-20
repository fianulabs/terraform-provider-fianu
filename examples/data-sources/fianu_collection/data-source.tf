# Look up an existing Fianu collection by its path. The entity is read
# only — Terraform never creates, changes or archives it.

data "fianu_collection" "existing" {
  path = "f.collection.example"
}

output "collection_uuid" {
  value = data.fianu_collection.existing.uuid
}
