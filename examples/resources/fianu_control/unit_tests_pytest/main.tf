# PyTest unit-test control rebuilt as native HCL.
#
# Source: official-controls/envs/dev/controls/unit.tests/testing.unit.pytest.results/
#
# Demonstrates the test-results measure shape: per-bucket maximums (failed,
# error, skipped) plus an `array.string` value for exception lists, and a
# total minimum. Also shows `documentation` and `results.not_required`.

resource "fianu_control" "unit_tests_pytest" {
  path = "testing.unit.pytest.results"
  name = "Unit Tests"

  detail = {
    full_name   = "PyTest Unit Test Cases"
    display_key = "PYTST"
    description = <<-EOT
      This control validates unit test execution results to ensure code quality
      before deployment. Deploying code with failing tests increases the risk
      of production defects and system instability.
    EOT

    documentation = [
      { title = "Pytest.org",                  url = "https://docs.pytest.org/" },
      { title = "Fianu Pytest Documentation", url = "https://docs.fianu.io/integrations/pytest/pytest_testing" },
    ]

    results = {
      fail         = true
      not_required = true
    }

    relations = [{
      domain     = "compliance.controls"
      collection = "testing"
      path       = "pytest.testing"
      note       = "occurrence"
      producer = {
        type = "plugin"
        path = "pytest"
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
        { name = "required", type = "metric", value = "bool" },
        {
          name        = "tests"
          type        = "section"
          description = "Description for node tests"
          children = [
            {
              name = "failed"
              type = "section"
              children = [
                { name = "maximum",    type = "metric", value = "number" },
                { name = "exceptions", type = "metric", value = "array.string" },
              ]
            },
            {
              name = "error"
              type = "section"
              children = [
                { name = "maximum",    type = "metric", value = "number" },
                { name = "exceptions", type = "metric", value = "array.string" },
              ]
            },
            {
              name = "skipped"
              type = "section"
              children = [
                { name = "maximum",    type = "metric", value = "number" },
                { name = "exceptions", type = "metric", value = "array.string" },
              ]
            },
            {
              name = "total"
              type = "section"
              children = [
                { name = "minimum", type = "metric", value = "number" },
              ]
            },
          ]
        },
      ]
    }

    evaluation = [
      { type = "rule",      engine = "opa", label = "rule.rego",      content = file("${path.module}/rule.rego") },
      { type = "rule_test",                 label = "rule_test.rego", content = file("${path.module}/rule_test.rego") },
      { type = "detail",                    label = "detail.py",      content = file("${path.module}/detail.py") },
      { type = "display",                   label = "display.py",     content = file("${path.module}/display.py") },
    ]
  }
}
