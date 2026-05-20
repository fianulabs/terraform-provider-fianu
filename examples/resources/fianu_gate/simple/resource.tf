# Simple gate: one policy variation (no criteria), one pod enforcing
# everywhere. The minimum-viable gate authoring shape.

resource "fianu_gate" "baseline_security" {
  path = "f.gate.security.baseline"
  name = "Baseline Security Gate"

  detail = {
    full_name   = "Baseline Security Gate"
    display_key = "BSEC"
    description = "Enforces a single security policy across all production assets."

    environments = [
      { path = "env.prod" },
    ]

    # Inline policy — deployed as a separate entities.Policy targeting this
    # gate. Auto-pathed at <gate.path>.policy (so f.gate.security.baseline.policy).
    policy = {
      variations = [
        {
          policy = jsonencode({
            required = true
            vulnerabilities = {
              critical = { maximum = 0 }
            }
          })
        },
      ]
    }

    pods = [
      {
        key              = "default"
        protection_level = "enforce"
      },
    ]
  }
}
