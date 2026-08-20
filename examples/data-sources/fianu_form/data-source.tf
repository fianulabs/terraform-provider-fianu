# Look up an existing Fianu form by its path. The entity is read
# only — Terraform never creates, changes or archives it.

data "fianu_form" "existing" {
  path = "f.form.example"
}

output "form_uuid" {
  value = data.fianu_form.existing.uuid
}
