resource "fianu_gate" "security" {
  path = "f.gate.security"
  name = "Security Gate"

  detail = {
    full_name   = "Production Security Gate"
    display_key = "PSEC"
    description = "Gates production deployments on critical security findings."

    environments = [
      { path = "env.prod" },
    ]

    policy = {
      variations = [
        { policy = jsonencode({ required = true }) },
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
