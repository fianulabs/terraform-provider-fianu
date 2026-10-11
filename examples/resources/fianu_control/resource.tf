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

# Approval rules for exceptions, counted per asset. The first exception on
# each asset for a given CVE approves itself; any repeat on that asset needs
# the control owner. Rules are evaluated in order and the first match wins.
resource "fianu_control" "dependabot_alerts" {
  path = "dependabot-alerts"
  name = "Dependabot Alerts"

  detail = {
    full_name   = "No open Dependabot alerts"
    display_key = "DBA"

    # A per-asset history rule must name a measure declared here: an
    # `array.string` list with no `key`, or an `array.object` list whose
    # `keys` include the rule's `key`.
    policy_template = {
      measures = [{
        name = "vulnerabilities"
        type = "section"
        children = [
          { name = "cves", type = "metric", value = "array.string" },
        ]
      }]
    }

    approvals = {
      rules = [{
        id            = "first-exception-per-asset"
        name          = "First exception per asset and CVE auto-approves"
        subject_types = ["exception"]
        # `vulnerabilities.cves` is a list of strings, so no `key`. Drop
        # `measure` to count every exception on the asset instead of per CVE.
        history  = { scope = "asset", measure = "vulnerabilities.cves" }
        criteria = { expressions = ["$history.occurrence == 1"] }
        action   = { type = "auto_approve" }
        }, {
        id            = "repeat-needs-owner"
        name          = "Repeat exceptions need the control owner"
        subject_types = ["exception"]
        action = {
          type         = "require"
          requirements = [{ label = "Control Owner", approvers = ["role:owner@control"], count = 1 }]
        }
      }]
    }
  }
}
