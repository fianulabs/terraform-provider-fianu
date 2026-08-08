# An environment is a named deployment stage. Its `matching` block decides
# which incoming deployment events belong to it.

resource "fianu_environment" "production" {
  path = "environments.production"
  name = "Production"

  detail = {
    description = "Customer-facing production."

    matching = {
      asset = { type = "repository" }
      expressions = [
        { expression = "deployment.environment == 'production'" },
      ]
    }
  }
}
