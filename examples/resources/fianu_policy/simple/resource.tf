# Simple policy: one variation that applies the control to every repository
# in scope. No expressions — the unscoped variation links to the default
# index for its asset type, matching all assets of that type.

resource "fianu_policy" "iac_scan_baseline" {
  path = "f.policy.security.iac.baseline"
  name = "Baseline IaC Scan Policy"

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
            high     = { maximum = 5 }
          }
        })
      },
    ]
  }
}
