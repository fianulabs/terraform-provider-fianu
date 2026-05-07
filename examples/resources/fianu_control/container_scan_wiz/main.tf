# Wiz container-scan control rebuilt as native HCL.
#
# Source: official-controls/envs/dev/controls/container.scan/wiz.containerscan.vulnerabilities/
#
# Demonstrates the four-severity vulnerability shape (critical/high/medium/low)
# with both `maximum` thresholds and `exceptions` list overrides. The asset
# scope is `artifact` only with multiple series (digest, uri, commit) — the
# kind of multi-series binding container scans need.

resource "fianu_control" "container_scan_wiz" {
  path = "wiz.containerscan.vulnerabilities"
  name = "Container Scan"

  detail = {
    full_name   = "Container Scan"
    display_key = "CSWIZ"
    description = <<-EOT
      This control validates container scan results to identify vulnerabilities
      in container images before deployment. Container vulnerabilities can
      provide attack vectors for container escape, privilege escalation, or
      data exfiltration.
    EOT

    results = {
      fail         = true
      not_required = true
    }

    relations = [{
      domain     = "compliance.controls"
      collection = "security"
      path       = "wiz.containerscan"
      note       = "occurrence"
      producer = {
        type = "plugin"
        path = "wiz-containerscan"
      }
    }]

    assets = [{
      type = "artifact"
      series = [
        { name = "digest" },
        { name = "uri" },
        { name = "commit" },
      ]
    }]

    policy_template = {
      measures = [
        { name = "required", type = "metric", value = "bool" },
        {
          name = "vulnerabilities"
          type = "section"
          children = [
            {
              name = "critical"
              type = "section"
              children = [
                { name = "maximum",    type = "metric", value = "number" },
                { name = "exceptions", type = "metric", value = "array.string" },
              ]
            },
            {
              name = "high"
              type = "section"
              children = [
                { name = "maximum",    type = "metric", value = "number" },
                { name = "exceptions", type = "metric", value = "array.string" },
              ]
            },
            {
              name = "medium"
              type = "section"
              children = [
                { name = "maximum",    type = "metric", value = "number" },
                { name = "exceptions", type = "metric", value = "array.string" },
              ]
            },
            {
              name = "low"
              type = "section"
              children = [
                { name = "maximum",    type = "metric", value = "number" },
                { name = "exceptions", type = "metric", value = "array.string" },
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
