# Look up an existing Fianu policy by its path. The entity is read
# only — Terraform never creates, changes or archives it.

data "fianu_policy" "existing" {
  path = "f.policy.example"
}

output "policy_uuid" {
  value = data.fianu_policy.existing.uuid
}
