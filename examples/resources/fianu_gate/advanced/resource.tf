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

    # Each variation's required_controls / required_gates list is resolved
    # to entity UUIDs at apply time and shipped as the gate's policy. The
    # variation's `criteria` decides which assets the requirement set
    # applies to; the first matching variation wins (priority-ordered).
    policy = {
      variations = [
        # Tier 1 — payments-team production repos. Both the heavy SAST
        # control and the unit-test control must pass.
        {
          criteria = {
            expressions = [
              { expression = "asset.properties.owner == 'team-payments'" },
            ]
          }
          required_controls = [
            "sast.checkmarx",
            "unit.tests.pytest",
          ]
        },

        # Tier 2 — staging assets get a lighter check (unit tests only).
        {
          criteria = {
            expressions = [
              { expression = "asset.scm.repository startsWith 'staging-'" },
            ]
          }
          required_controls = ["unit.tests.pytest"]
        },

        # Tier 3 — catch-all: depend on the upstream "must merge to main"
        # gate. Chain gates with required_gates rather than required_controls.
        {
          required_gates = ["f.gate.merge.policy"]
        },
      ]
      override = {
        asset = {
          types = ["repository"]
        }
      }
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
