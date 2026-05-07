# SAST control rebuilt as native HCL.
#
# Source: official-controls/envs/dev/controls/sast/checkmarx.sast.vulnerabilities/
#
# Every spec.yaml field maps 1:1 to an HCL attribute. Rego and Python content
# stays in its native files (rule.rego, detail.py, display.py, rule_test.rego)
# and is loaded via `file()` so syntax highlighting / linters keep working.
#
# When this resource is applied, the provider builds the same `*entities.Control`
# struct that `fianu console deploy ./checkmarx.sast.vulnerabilities/` would
# build server-side from the on-disk package — both paths terminate at the
# same `pkg/entities_files/control_deployer.go::DeployFromRawContent` and
# honour the same SHA256 idempotency gate.

resource "fianu_control" "sast_checkmarx" {
  path = "checkmarx.sast.vulnerabilities"
  name = "SAST"

  detail = {
    full_name   = "Static Asset Security Analysis"
    display_key = "CHXST"
    description = <<-EOT
      This control validates SAST scan results to identify security vulnerabilities
      in source code. Unaddressed code vulnerabilities can be exploited by attackers
      to compromise application security, expose sensitive data, or enable
      unauthorized access.
    EOT

    results = {
      fail = true
    }

    relations = [{
      domain     = "compliance.controls"
      collection = "security"
      path       = "checkmarx.sast"
      note       = "occurrence"
      producer = {
        type = "plugin"
        path = "checkmarx"
      }
    }]

    assets = [
      {
        type = "module"
        series = [
          { name = "commit" },
          { name = "tag" },
        ]
      },
      {
        type = "repository"
        series = [
          { name = "commit" },
          { name = "tag" },
        ]
      },
      {
        type = "artifact"
        series = [
          { name = "commit" },
          { name = "tag" },
        ]
      },
    ]

    policy_template = {
      measures = [
        {
          name  = "vulnerabilities"
          type  = "section"
          children = [
            {
              name  = "critical"
              type  = "section"
              children = [
                { name = "maximum", type = "metric", value = "number" },
              ]
            },
            {
              name  = "high"
              type  = "section"
              children = [
                { name = "maximum", type = "metric", value = "number" },
              ]
            },
          ]
        },
        {
          name  = "required"
          type  = "metric"
          value = "bool"
        },
      ]
    }

    evaluation = [
      {
        type    = "rule"
        engine  = "opa"
        label   = "rule.rego"
        content = file("${path.module}/rule.rego")
      },
      {
        type    = "rule_test"
        label   = "rule_test.rego"
        content = file("${path.module}/rule_test.rego")
      },
      {
        type    = "detail"
        label   = "detail.py"
        content = file("${path.module}/detail.py")
      },
      {
        type    = "display"
        label   = "display.py"
        content = file("${path.module}/display.py")
      },
    ]
  }
}
