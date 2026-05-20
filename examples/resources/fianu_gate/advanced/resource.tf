# Advanced gate: tiered policy variations with CEL criteria + a per-scope
# pod that flips production-only into enforce while leaving everything else
# at check-mode. Demonstrates the full surface: identity, environments,
# inline policy with variation criteria, and pipeline-automation pods with
# scoped overrides.
#
# CEL note: combine clauses inside a single expression with `&&`/`||`.
# Multiple list entries are only needed when mixing OR semantics across
# fundamentally different predicates.

resource "fianu_gate" "tiered_security" {
  path = "f.gate.security.tiered"
  name = "Tiered Security Gate"

  detail = {
    full_name   = "Tiered Security Gate"
    display_key = "TSEC"
    description = "Strict for prod-owned-by-payments, relaxed for previews, gentle elsewhere."

    environments = [
      { path = "env.prod" },
      { path = "env.staging" },
    ]

    policy = {
      variations = [
        # Tier 1 — payments-team production repos, strict thresholds.
        {
          criteria = {
            expressions = [
              { expression = "asset.labels exists tier && asset.labels.tier == 'prod' && asset.properties.owner == 'team-payments'" },
            ]
          }
          policy = jsonencode({
            required = true
            vulnerabilities = {
              critical = { maximum = 0 }
              high     = { maximum = 0 }
              medium   = { maximum = 2 }
            }
          })
        },

        # Tier 2 — preview/staging branches under payments repos.
        {
          criteria = {
            expressions = [
              { expression = "asset.scm.repository matches '^my-org/payments-.+' && (asset.identifier startsWith 'preview-' || asset.identifier startsWith 'staging-')" },
            ]
          }
          policy = jsonencode({
            required = true
            vulnerabilities = {
              critical = { maximum = 0 }
              high     = { maximum = 3 }
            }
          })
        },

        # Tier 3 — catch-all.
        {
          policy = jsonencode({
            required = true
            vulnerabilities = {
              critical = { maximum = 0 }
              high     = { maximum = 10 }
              medium   = { maximum = 25 }
            }
          })
        },
      ]
    }

    pods = [
      # Default rule — enforce everywhere unless a more specific pod
      # overrides for a given scope.
      {
        key              = "default"
        name             = "Default enforcement"
        protection_level = "enforce"
        enabled          = true
      },

      # Scoped rule — staging/preview repos run in check mode (the gate
      # evaluates but never blocks). Production keeps default enforce.
      {
        key              = "staging-relaxed"
        name             = "Staging check-only override"
        protection_level = "enforce"
        matching = [
          {
            protection_level = "check"
            expressions = [
              { expression = "asset.scm.repository startsWith 'staging-' || asset.scm.repository startsWith 'preview-'" },
            ]
          },
        ]
      },
    ]
  }
}
