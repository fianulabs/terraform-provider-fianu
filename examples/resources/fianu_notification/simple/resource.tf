# Minimum viable: turn a notification bucket on for a gate. Recipients and
# channels fall back to whatever is configured at a broader scope.

resource "fianu_notification" "gate_blocked" {
  entity_uuid = fianu_gate.deploy.uuid
  type        = "notification_blocking"
  enabled     = true
}
