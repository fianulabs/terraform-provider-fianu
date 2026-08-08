# Notify a control's owners when its attestations start failing.
#
# One resource configures one notification *bucket*, and several emitted
# notification types share a bucket — notification_attestation_failure covers
# attestation fail, warn, not-found, error, and recovery.

resource "fianu_notification" "sast_failures" {
  entity_uuid = fianu_control.sast.uuid
  type        = "notification_attestation_failure"

  enabled    = true
  urgency    = 4
  mode       = "transition" # only on the pass -> fail edge, not every run
  recipients = ["control_owner", "asset_owner"]
  channels   = ["email", "slack"]
}
