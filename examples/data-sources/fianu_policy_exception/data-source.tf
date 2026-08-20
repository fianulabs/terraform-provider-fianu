# Look up an existing Fianu policy exception by its path. The entity is read
# only — Terraform never creates, changes or archives it.

data "fianu_policy_exception" "existing" {
  path = "f.policy_exception.example"
}

output "policy_exception_uuid" {
  value = data.fianu_policy_exception.existing.uuid
}
