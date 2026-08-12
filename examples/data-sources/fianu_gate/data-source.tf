# Look up an existing Fianu gate by its path. The entity is read
# only — Terraform never creates, changes or archives it.

data "fianu_gate" "existing" {
  path = "f.gate.example"
}

output "gate_uuid" {
  value = data.fianu_gate.existing.uuid
}
