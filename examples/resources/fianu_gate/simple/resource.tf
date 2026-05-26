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
    # gate at the same entity_key. The gate's policy template is fixed to
    # a single `controls` measure; the provider builds the wire payload
    # from required_controls / required_gates by resolving each entry to
    # its entity UUID at apply time.
    policy = {
      variations = [
        {
          required_controls = ["terraform.example.iac.scan"]
        },
      ]
      override = {
        asset = {
          types = ["repository"]
        }
      }
    }

    pods = [
      {
        key              = "default"
        protection_level = "enforce"
      },
    ]
  }
}
