# A collection groups controls under a domain (domain -> collection -> control).
#
# `domain` is the parent domain's entity UUID, not a path — the server does no
# path resolution for it. Domains are managed in the Console.

resource "fianu_collection" "appsec" {
  path = "collections.appsec"
  name = "Application Security"

  detail = {
    description                = "SAST, SCA, and container scanning controls."
    domain                     = "d0a1b2c3-0000-4000-8000-000000000001"
    inherit_domain_permissions = true
  }
}
