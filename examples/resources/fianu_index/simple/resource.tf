# Simple index: a single CEL predicate identifying production repos.
# Defaults to combine_with = "AND" (irrelevant with a single expression)
# and kind = "write" (recompute on referenced-asset change).
resource "fianu_index" "prod_repos" {
  path = "f.indexes.repos.prod"
  name = "Production Repositories"

  detail = {
    asset_type = "repository"
    expressions = [
      { source = "asset.scm.repository startsWith 'prod-'" },
    ]
  }
}
