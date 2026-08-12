# Look up a platform this configuration does not manage, and use its UUID.
#
# `fianu_instance.detail.platform_uuid` needs a platform's server-generated
# UUID. When the same configuration creates the platform you can write
# `fianu_platform.jira.uuid`. When it does not — the platform was created in
# the console, by `fianu console deploy`, or by another team's state file —
# this data source resolves the UUID from the path instead of hardcoding it.
#
# Lookup is by `path` because that is the stable identifier humans author.
# A hardcoded UUID is opaque, and points at nothing once the entity is
# archived and recreated.

data "fianu_platform" "jira" {
  path = "f.platform.jira"
}

resource "fianu_instance" "acme_jira" {
  path = "f.instance.jira.acme"
  name = "Acme Jira"

  detail = {
    description   = "Acme's production Jira"
    platform_uuid = data.fianu_platform.jira.uuid

    domains = [
      {
        host        = "acme.atlassian.net"
        scheme      = "https"
        designation = "api"
        base_path   = "/rest/api/3"
      },
    ]
  }
}
