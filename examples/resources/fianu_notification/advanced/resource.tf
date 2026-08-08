# Scoped, tuned notifications — every knob the resource exposes.
#
# The three examples below show the three axes of control:
#   1. `rules`   — which assets the notification fires for
#   2. `filters` — which sub-events within the bucket fire
#   3. `params`  — the tuning knobs for computed/periodic buckets

# 1 + 2 — attestation failures, but only on production repos, and only for the
# sub-events we care about.
#
# `rules` is the same asset matcher as fianu_policy variation criteria and
# fianu_gate check matching: a CEL expression group, a reference to existing
# fianu_index entities, or a bare asset type.
resource "fianu_notification" "prod_attestation_failures" {
  entity_uuid = fianu_control.sast.uuid
  type        = "notification_attestation_failure"

  enabled    = true
  urgency    = 5
  mode       = "transition"
  recipients = ["control_owner", "control_manager", "asset_owner"]
  channels   = ["email", "slack"]

  rules = {
    asset = { type = "repository" }
    expressions = [
      { expression = "asset.scm.repository startsWith 'prod-'" },
    ]
  }

  # Filters are read VERBATIM: a key you do not list is a filter left off.
  # List every sub-event you want enabled.
  filters = {
    gate_blocking    = { enabled = true, urgency = 5 }
    release_blocking = { enabled = true, urgency = 5 }
    recovery         = { enabled = true, urgency = 2 }
    tagged_commits   = { enabled = true }
    on_any_commit    = { enabled = false }
  }
}

# Scoping by an existing index instead of inline CEL — reuse the asset universe
# a fianu_index already defines.
resource "fianu_notification" "sox_repo_failures" {
  entity_uuid = fianu_control.sast.uuid
  type        = "notification_persistent_failure"

  enabled    = true
  urgency    = 4
  recipients = ["control_owner"]
  channels   = ["email"]

  rules = {
    indexes = [
      { path = fianu_index.sox_repos.path },
    ]
  }

  # 3 — tuning for a computed/periodic bucket: only fire once a failure has
  # persisted a week.
  params = {
    duration_days = 7
  }
}

# A muted-until notification: configured and ready, suppressed through the
# end of a planned migration window.
resource "fianu_notification" "gate_structure_changes" {
  entity_uuid = fianu_gate.deploy.uuid
  type        = "notification_gate_structure_change"

  enabled     = true
  muted_until = "2026-09-01T00:00:00Z"
  recipients  = ["gate_owner", "gate_manager"]
  channels    = ["in_app"]
}
