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
            medium   = { maximum = 5 }
            low      = { maximum = 20 }
          }
        })
      },
    ]
  }
}
