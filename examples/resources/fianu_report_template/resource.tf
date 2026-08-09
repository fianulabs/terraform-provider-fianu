# A report template composes control templates into a full report layout.
#
# Note the import prefix and resource id use `template/`, not
# `report_template/` — the server's entity type for this is the string
# "template".

resource "fianu_report_template" "soc2_type_ii" {
  path = "f.template.soc2.type2"
  name = "SOC 2 Type II"

  detail = {
    description    = "Annual SOC 2 Type II evidence package"
    output_formats = ["pdf", "html"]

    layout_config = jsonencode({
      header = "soc2.header"
      footer = "soc2.footer"
      sections = [
        {
          title    = "CC6 — Logical Access"
          controls = ["okta.mfa.enforced", "okta.offboarding.completed"]
        },
        {
          title    = "CC8 — Change Management"
          controls = ["github.pr.approved", "checkmarx.sast.vulnerabilities"]
        },
      ]
    })
  }
}
