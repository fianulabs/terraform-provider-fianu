# Advanced policy: per-variation CEL criteria with per-criteria asset
# binding. Three tiers split the asset population by repo metadata. CEL
# syntax matches the form the Fianu Console accepts in the UI — the
# server parses each `expression` string through external/pkg/cel.ParseExpression
# on deploy.
#
# Variations evaluate top-down; the first matching variation wins.
#
# Each criteria with `expressions` carries its own `asset` binding — the
# server spawns one private content-addressed index per criteria.
# Compose clauses inside a single CEL string with `&&` / `||`; multiple
# list entries only matter when mixing `combine_with = "OR"` semantics
# across fundamentally different predicates.

resource "fianu_policy" "iac_scan_tiered" {
  path = "f.policy.security.iac.tiered"
  name = "Tiered IaC Scan Policy"

  detail = {
    type = "standard"

    control = {
      path = "terraform.example.iac.scan"
    }

    variations = [
      # Tier 1 — strictly-enforced production repos. Compound predicate:
      # labelled tier=prod, owned by the payments team, and not a mobile app
      # (those have their own policy elsewhere).
      {
        criteria = {
          asset = { type = "repository" }
          expressions = [
            { expression = "asset.labels exists tier && asset.labels.tier == 'prod' && asset.properties.owner == 'team-payments' && asset.scm.repository not matches '(?i).*(ios|android|appco).*'" },
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

      # Tier 2 — preview/staging branches under any payments repo
      # ("my-org/payments-*"). Regex on the SCM repo identifier, combined
      # with an OR over the two staging-flavour prefixes.
      {
        criteria = {
          asset = { type = "repository" }
          expressions = [
            { expression = "asset.scm.repository matches '^my-org/payments-.+' && (asset.identifier startsWith 'preview-' || asset.identifier startsWith 'staging-')" },
          ]
        }
        policy = jsonencode({
          required = true
          vulnerabilities = {
            critical = { maximum = 0 }
            high     = { maximum = 3 }
            medium   = { maximum = 10 }
          }
        })
      },

      # Tier 3 — catch-all for everything else. No expressions means the
      # variation matches every asset of the bound type (links the default
      # index for that type).
      {
        criteria = {
          asset = { type = "repository" }
        }
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
}
