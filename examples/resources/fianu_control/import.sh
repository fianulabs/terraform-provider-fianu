#!/usr/bin/env bash
# Import an existing Fianu control into Terraform state.
# The composite ID is `<entity_type>/<entity_key>` — the same form `terraform
# state list` will display.

terraform import fianu_control.payment_service_sast control/payment-service-sast
