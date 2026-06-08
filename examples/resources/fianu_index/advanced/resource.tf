# Advanced index: every knob set. Two expressions OR'd over the repo
# universe, with `application` as a dependent asset type so the worker
# invalidates membership when an application's metadata changes.
#
# `write-ahead` precomputes membership rather than waiting for an
# asset-change event — preferred for indexes referenced by hot evaluation
# paths (gates that block deployments).

resource "fianu_index" "sox_repos" {
  path = "f.indexes.repos.sox"
  name = "SOX Repositories"

  detail = {
    description           = "Repos under SOX scope — either explicitly tagged or owned by an applicable team."
    asset_type            = "repository"
    dependent_asset_types = ["application"]
    combine_with          = "OR"
    kind                  = "write-ahead"
    expressions = [
      { source = "asset.labels exists scope && asset.labels.scope == 'sox'" },
      { source = "asset.properties.owner in ['team-payments', 'team-billing', 'team-treasury']" },
    ]
  }
}

# Reference the index from a policy. The variation has no inline CEL —
# the linked index already carries the asset type and expressions.
resource "fianu_policy" "sox_strict_scan" {
  path = "f.policy.security.sox.strict"
  name = "Strict Scan — SOX Repos"

  detail = {
    type = "standard"
    control = {
      path = "terraform.example.iac.scan"
    }
    variations = [
      {
        criteria = {
          indexes = [
            { path = fianu_index.sox_repos.path },
          ]
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

# Same index, reused on a gate's protected scope. One index entity, many
# consumers — that's the point.
resource "fianu_gate" "sox_deploy_gate" {
  path = "f.gates.deploy.sox"
  name = "SOX Deployment Gate"

  detail = {
    full_name   = "SOX Deployment Gate"
    display_key = "SOXG"

    policy = {
      variations = [
        {
          criteria = {
            indexes = [
              { path = fianu_index.sox_repos.path },
            ]
          }
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
        matching = [
          {
            # check-mode for staging branches inside the SOX universe —
            # still surfaces violations but doesn't block the deploy.
            protection_level = "check"
            indexes = [
              { path = fianu_index.sox_repos.path },
            ]
          },
        ]
      },
    ]
  }
}
