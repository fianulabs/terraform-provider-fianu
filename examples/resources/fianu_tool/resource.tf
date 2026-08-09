# A tool is the integration that produces the evidence controls evaluate.
# `sources.produces` is what makes that output addressable: a control's
# evaluation input has to resolve to something a registered tool publishes, or
# the deploy is rejected.

resource "fianu_tool" "checkmarx" {
  path = "f.tool.checkmarx"
  name = "Checkmarx SAST"

  detail = {
    description  = "Static application security testing"
    key          = "checkmarx"
    tool_type    = "sast"
    tool_version = "9.5"

    sources = {
      produces = [
        {
          path = "checkmarx.sast.vulnerabilities"
          note = "occurrence"

          integration = {
            name = "checkmarx"
            type = "tool"
          }

          schema = jsonencode({
            type = "object"
            properties = {
              scanId = { type = "string" }
              findings = {
                type = "array"
                items = {
                  type = "object"
                  properties = {
                    severity = { type = "string" }
                    ruleId   = { type = "string" }
                  }
                }
              }
            }
          })
        },
      ]

      consumes = [
        {
          path = "scm.repository.commit"
          note = "origin"
        },
      ]
    }
  }
}
