# Look up an existing Fianu report template by its path. The entity is read
# only — Terraform never creates, changes or archives it.

data "fianu_report_template" "existing" {
  path = "f.report_template.example"
}

output "report_template_uuid" {
  value = data.fianu_report_template.existing.uuid
}
