# Simple policy: one variation that applies the control to every repository
# in scope. No criteria — the variation matches all assets of the target
# types. Good starting point for "I want this control enforced everywhere."

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
        policy = jsonencode({
          required = true
          vulnerabilities = {
            critical = { maximum = 0 }
            high     = { maximum = 5 }
          }
        })
      },
    ]

    override = {
      asset = {
        types = ["repository"]
      }
    }
  }
}
