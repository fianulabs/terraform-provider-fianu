# An instance is a configured, reachable deployment of a platform: the Jira at
# acme.atlassian.net, not "Jira" the product. The platform declares the
# operational contract (endpoint defaults, health probes, error semantics); the
# instance says where it lives.
#
# Credentials are NOT managed here. They are secrets held in an external secret
# manager, with their own rotation lifecycle and their own endpoints — putting
# them in an entity version would mint a new instance version on every rotation
# and write secret references into entity history.

resource "fianu_platform" "jira" {
  path = "f.platform.jira"
  name = "Jira"

  detail = {
    tool_version = "3"
  }
}

resource "fianu_instance" "acme_jira" {
  path = "f.instance.jira.acme"
  name = "Acme Jira"

  detail = {
    description   = "Acme's production Jira"
    platform_uuid = fianu_platform.jira.uuid

    # Domains are carried on the detail, not as child entities: a domain has no
    # identity outside the instance version that declares it. The server stamps
    # a fresh uuid on each one every time the instance is written.
    domains = [
      {
        host        = "acme.atlassian.net"
        scheme      = "https"
        designation = "api"
        base_path   = "/rest/api/3"

        # The runtime selects a domain by matching these against what a job
        # needs.
        utilities = ["issues", "projects"]
      },
      {
        host        = "acme.atlassian.net"
        scheme      = "https"
        designation = "ui"
      },
    ]
  }
}
