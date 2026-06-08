resource "fianu_index" "prod_repos" {
  path = "f.indexes.repos.prod"
  name = "Production Repositories"

  detail = {
    description = "All repositories tagged tier=prod, owned by an engineering team."
    asset_type  = "repository"
    expressions = [
      { source = "asset.labels exists tier && asset.labels.tier == 'prod'" },
      { source = "asset.properties.owner startsWith 'team-'" },
    ]
  }
}
