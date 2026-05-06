resource "fianu_control" "payment_service_sast" {
  path = "payment-service-sast"
  name = "Payment Service SAST"

  detail = {
    full_name   = "Payment Service Static Analysis"
    display_key = "PSSAST"
    description = "Static analysis gates for the payment service repository."
  }
}
