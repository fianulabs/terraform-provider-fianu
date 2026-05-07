package rule

default fail = false
default notFound = false
default notRequired = false
default pass = false

import future.keywords

isException(v, exceptions) if {
  some exception
  exception = exceptions[_]
  exception == v.nodeid
}

isTestConditionMet(level, v, exceptions) = false if {
  v.outcome == level
  not isException(v, exceptions)
}

isTestConditionMet(level, v, exceptions) = true if {
  isException(v, exceptions)
}

pass if {
  detail := input.detail

  failed := count([
    v | v := detail.tests[_];
    check := isTestConditionMet("failed", v, data.tests.failed.exceptions);
    fianu.record_violation(check, v);
    not check
  ])

  error := count([
    v | v := detail.tests[_];
    check := isTestConditionMet("error", v, data.tests.error.exceptions);
    fianu.record_violation(check, v);
    not check
  ])

  skipped := count([
    v | v := detail.tests[_];
    check := isTestConditionMet("skipped", v, data.tests.skipped.exceptions);
    fianu.record_violation(check, v);
    not check
  ])

  total := count(detail.tests)

  log(sprintf("test-results: failed=%v error=%v skipped=%v total=%v", [failed, error, skipped, total]))

  failed <= data.tests.failed.maximum
  error <= data.tests.error.maximum
  skipped <= data.tests.skipped.maximum

  count(detail.tests) >= data.tests.total.minimum
}

notRequired if {
  not pass
  data.required == false
}
