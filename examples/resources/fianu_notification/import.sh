# Notification pods are addressed by the (entity, pod_type, key) triple.
# The key is "config" unless you overrode it.
terraform import fianu_notification.sast_failures '<entity_uuid>/notification_attestation_failure/config'
