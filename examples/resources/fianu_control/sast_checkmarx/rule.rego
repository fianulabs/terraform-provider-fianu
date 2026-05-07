package rule

default pass = false
default fail = false
default notFound = false

import future.keywords

isCritical(v) if {
  v == "Critical"
}

isHigh(v) if {
  v == "High"
}

isHigh(v) if {
  v == "error"
}

pass if {
  detail := input.detail

  critical := count({v | v := detail.vulnerabilities[_]; isCritical(v.severity)})
  high := count({v | v := detail.vulnerabilities[_]; isHigh(v.severity)})

  log(sprintf("critical vulnerabilities: %v, high vulnerabilities: %v", [critical, high]))

  critical <= data.vulnerabilities.critical.maximum
  high <= data.vulnerabilities.high.maximum
}