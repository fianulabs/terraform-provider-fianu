# Attach a platform-capability pod to a gate, letting JFrog act as a gating
# source for this gate's checks.
#
# fianu_entity_pod is the generic form: `value` is whatever JSON the pod type
# expects. For notification pods use fianu_notification instead — it validates
# the same payload and exposes it as real HCL.

resource "fianu_entity_pod" "jfrog_gating" {
  entity_uuid = fianu_gate.deploy.uuid
  pod_type    = "platforms_capabilities_data_exports_gating"
  key         = "gatingSource:jf-prod"

  name        = "JFrog production gating"
  description = "Defer this gate's verdict to JFrog's own evaluation."

  value = jsonencode({
    capability  = "gatingSource"
    instanceKey = "jf-prod"
  })
}
