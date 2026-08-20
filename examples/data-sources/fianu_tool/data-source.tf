# Look up an existing Fianu tool by its path. The entity is read
# only — Terraform never creates, changes or archives it.

data "fianu_tool" "existing" {
  path = "f.tool.example"
}

output "tool_uuid" {
  value = data.fianu_tool.existing.uuid
}
