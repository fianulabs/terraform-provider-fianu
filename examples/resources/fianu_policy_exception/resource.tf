# An exception waives a control's requirements for a narrower slice of assets
# than the policy it overrides. Same shape as fianu_policy — same control
# binding, same variations — but stored as its own entity type, and it takes
# precedence over standard policies when both match an asset.
#
# `detail.type` is omitted: an exception has exactly one legal value and the
# provider supplies it.

resource "fianu_policy" "iac_scan_strict" {
  path = "f.policy.security.iac.strict"
  name = "Strict IaC Scan Policy"

  detail = {
    type = "standard"

    control = {
      path = "terraform.example.iac.scan"
    }

    variations = [
      {
        criteria = {
          asset = { type = "repository" }
        }
        policy = jsonencode({
          required = true
          vulnerabilities = {
            critical = { maximum = 0 }
            high     = { maximum = 0 }
          }
        })
      },
    ]
  }
}

# Legacy repositories are exempt from the critical/high gate until the
# migration lands. Higher priority wins, so this beats the policy above for
# the assets its criteria match.
resource "fianu_policy_exception" "legacy_repos" {
  path = "f.exception.security.iac.legacy"
  name = "IaC Scan Exception — Legacy Repositories"

  detail = {
    control = {
      path = "terraform.example.iac.scan"
    }

    variations = [
      {
        effect   = "exempt"
        priority = 100

        criteria = {
          asset = { type = "repository" }
          expressions = [
            { expression = "asset.scm.repository startsWith 'legacy-'" },
          ]
        }

        policy = jsonencode({
          required = false
        })
      },
    ]
  }
}
