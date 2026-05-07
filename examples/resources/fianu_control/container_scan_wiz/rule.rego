package rule

default fail = false
default notFound = false
default notRequired = false
default pass = false

import future.keywords

# checks the level matches and a fix version exists
isLevel(level, vuln) = true if {
  level == vuln.severity
  vuln.fixedVersion != ""
}

isException(vuln, exceptions) = true if {
  some exception
  exception = exceptions[_]
  exception == vuln.name
}

isException(vuln, exceptions) = true if {
  every location in vuln.locations
  {
    some exclusion
    exclusion = data.exclusions.locations[_]
    exclusion == location.physicalLocation.artifactLocation.uri
  }
}

isOk(level, vuln, exceptions) = false if {
  isLevel(level, vuln)
  not isException(vuln, exceptions)
}

isOk(level, vuln, exceptions) = true if {
  not isLevel(level, vuln.level)
}

isOk(level, vuln, exceptions) = true if {
  isException(vuln, exceptions)
}

# Evaluates the overall policy compliance based on critical and high vulnerabilities
pass if {
  detail := input.detail

  critical := count([
    	v | v := detail.vulnerabilities[_];
      check := isOk("CRITICAL", v, data.vulnerabilities.critical.exceptions);
      fianu.record_violation(check, v);
      not check])

  high := count([
    	v | v := detail.vulnerabilities[_];
      check := isOk("HIGH", v, data.vulnerabilities.critical.exceptions);
      fianu.record_violation(check, v);
      not check])

  medium := count([
    	v | v := detail.vulnerabilities[_];
      check := isOk("MEDIUM", v, data.vulnerabilities.critical.exceptions);
      fianu.record_violation(check, v);
      not check])

  low := count([
    	v | v := detail.vulnerabilities[_];
      check := isOk("LOW", v, data.vulnerabilities.critical.exceptions);
      fianu.record_violation(check, v);
      not check])
  info := count([
    	v | v := detail.vulnerabilities[_];
      check := isOk("INFO", v, data.vulnerabilities.critical.exceptions);
      fianu.record_violation(check, v);
      not check])

  # Count the number of critical and high vulnerabilities that meet filtering criteria
  log(sprintf("critical vulnerabilities: %v, high vulnerabilities: %v", [critical, high]))

  allLow := low + info

  # check if critical and high vulnerability counts are within limits
  critical <= data.vulnerabilities.critical.maximum
  high <= data.vulnerabilities.high.maximum
  medium <= data.vulnerabilities.medium.maximum
  allLow <= data.vulnerabilities.low.maximum
}

# Determines if the requirement for compliance is not required
notRequired if {
	data.required == false
	not pass
}