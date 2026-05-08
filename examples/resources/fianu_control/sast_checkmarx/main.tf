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

# DRY the evaluation cases into a local so the resource and the
# fianu_control_test action below share one source of truth.
locals {
  sast_checkmarx_evaluation = [
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
    # Test fixtures — vendored from the source control's input/ + data/.
    # The server's /entities/artifacts/test endpoint runs the rule case
    # above against each input/data pair and returns a JUnit-shaped report.
    {
      type    = "input"
      label   = "occ_case_1.json"
      content = file("${path.module}/input/occ_case_1.json")
    },
    {
      type    = "data"
      label   = "policy_case_1.json"
      content = file("${path.module}/data/policy_case_1.json")
    },
  ]
}

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

    evaluation = local.sast_checkmarx_evaluation
  }

  # When the control changes, automatically run its rego rules against the
  # vendored input/data fixtures. Failed cases surface as apply errors;
  # successful runs stream per-case "✓ occ_case_1" progress events to the
  # CLI. Equivalent to `fianu console test controls ./checkmarx.sast.vulnerabilities/`.
  lifecycle {
    action_triggers {
      events  = [after_create, after_update]
      actions = [action.fianu_control_test.sast_checkmarx]
    }
  }
}

# Run on demand:
#   terraform action fianu_control_test.sast_checkmarx
#
# Or watch it run as part of every apply via the action_triggers above.
action "fianu_control_test" "sast_checkmarx" {
  config {
    path       = "checkmarx.sast.vulnerabilities"
    name       = "SAST"
    evaluation = local.sast_checkmarx_evaluation
  }
}
