# A platform is the integration product instances connect to. Everything here
# is the contract every instance of it inherits — endpoint defaults, health
# probes, credential rotation, error semantics, audit policy — so getting it
# right once saves repeating it per instance.
#
# `platform_type` references a Console-managed platform type by UUID, the same
# way fianu_collection references a domain.

resource "fianu_platform" "github" {
  path = "f.platform.github"
  name = "GitHub"

  detail = {
    description  = "GitHub source control and CI"
    display_logo = "github"
    website_url  = "https://github.com"
    docs_url     = "https://docs.github.com"
    tool_version = "2024.11"

    platform_type = {
      name = "Source Control"
      uuid = var.source_control_platform_type_uuid
    }

    features = {
      webhooks = "true"
      apps     = "true"
    }

    sources = {
      produces = [
        { path = "github.repository.commit", note = "origin" },
        { path = "github.pull_request.review", note = "attestation" },
      ]
    }

    endpoint_defaults = {
      base_url = "https://api.github.com"
      notes    = "Public GitHub. Enterprise Server instances override per instance."
      default_headers = jsonencode({
        Accept = "application/vnd.github+json"
      })
    }

    # Probes deciding whether an instance is usable. `critical` failures take
    # the instance out of service rather than just annotating it.
    health_checks = [
      {
        check_key         = "api_reachable"
        description       = "API answers and the token still has quota"
        http_method       = "GET"
        endpoint_template = "/rate_limit"
        interval_seconds  = 60
        timeout_ms        = 5000
        retry_max         = 3
        retry_backoff_ms  = 250
        severity          = "critical"
        enabled           = true
        success_predicate = jsonencode({ status_in = [200] })
      },
    ]

    credential_policy = {
      rotation             = "proactive"
      grace_period_seconds = 300
      notes                = "App installation tokens expire hourly."
      reauth_triggers      = jsonencode({ on_status = [401] })
    }

    # Turns provider-specific failures into Fianu's error semantics, so the
    # collector knows whether to back off, re-auth, or give up.
    error_mappings = [
      {
        http_status    = 429
        classification = "rate_limit"
        action         = "backoff"
        is_terminal    = false
      },
      {
        http_status    = 401
        classification = "auth"
        action         = "reauth"
        is_terminal    = false
      },
      {
        http_status    = 404
        classification = "not_found"
        action         = "fail"
        is_terminal    = true
        endpoint_glob  = "/repos/*"
      },
    ]

    audit_policy = {
      level              = "info"
      pii_handling       = "redact"
      redact_fields      = ["author_email", "committer_email"]
      retention_days     = 90
      export_destination = "s3://fianu-logs/github/"
      sampling_rate      = 0.25
    }
  }
}

variable "source_control_platform_type_uuid" {
  type        = string
  description = "UUID of the Console-managed 'Source Control' platform type."
}
