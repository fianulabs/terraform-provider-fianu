resource "fianu_control" "payment_service_sast" {
  path = "payment-service-sast"
  name = "Payment Service SAST"

  detail = {
    full_name   = "Payment Service Static Analysis"
    display_key = "PSSAST"
    description = "Static analysis gates for the payment service repository."
  }
}

# Attaching an attestation form. The questionnaire itself is a fianu_form;
# `detail.form` only references it, and the deploy resolves the reference to
# the form's current active version.
#
# Removing this block does not detach the form — a deploy that omits the
# section means "unchanged" server-side. Detach in the Console.
resource "fianu_control" "vendor_review" {
  path = "vendor-annual-review"
  name = "Vendor Annual Review"

  detail = {
    full_name   = "Vendor Annual Security Review"
    display_key = "VASR"

    form = {
      path = fianu_form.vendor_review.path
    }
  }
}
