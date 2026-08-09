# A form is the reusable questionnaire an attestation presents. This resource
# manages the form *definition*; the answers someone submits are a form
# instance and belong to the attestation that collected them.
#
# Element order is meaningful. The server assigns each element a `code` from
# its position and matches submitted answers to questions by that code, so
# reordering `elements` re-points answers already collected — append rather
# than insert.

resource "fianu_form" "vendor_review" {
  path = "f.form.vendor.annual_review"
  name = "Annual Vendor Security Review"

  detail = {
    display_key = "vendor-review"
    description = "Completed by the control owner for each in-scope vendor"

    elements = [
      {
        name        = "Reviewer name"
        type        = "text"
        required    = true
        description = "Full legal name of the person signing off"

        # `text` options take a Go regexp; `expression` is required whenever
        # `validation` is true.
        options = jsonencode({
          validation = true
          expression = "^[A-Za-z ]+$"
        })
      },
      {
        name     = "Scope of review"
        type     = "blob"
        required = true

        options = jsonencode({
          placeholder = "Which systems and data did this vendor touch?"
        })
      },
      {
        name = "Residual risk"
        type = "radio"

        options = jsonencode({
          values = {
            high   = "High"
            medium = "Medium"
            low    = "Low"
          }
        })
      },
      {
        name = "SOC 2 report on file?"
        type = "boolean"

        options = jsonencode({
          values = {
            yes = "Yes"
            no  = "No"
          }
        })
      },
    ]
  }
}
