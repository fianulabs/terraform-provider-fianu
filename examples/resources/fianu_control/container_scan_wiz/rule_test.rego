package rule

import future.keywords

test_fail_occ_case_1 if {
    not pass with input as data.input.occ_case_1
              with data.required as data.data.policy_case_1.required
              with data.vulnerabilities as data.data.policy_case_1.vulnerabilities
}
