def parse_issue(issue):
  return {
    'severity': issue['severity']
  }
  
def parse_issues(issues):
  _issues = []
  
  for issue in issues:
    _issues.append(parse_issue(issue)) 

  return _issues
  
def to_summary(issues):
  # Initialize a counter for high severity vulnerabilities
  high_severity_count = 0
  critical_severity_count = 0
  medium_severity_count = 0
  low_severity_count = 0

  for issue in issues:
    # Check the severity of the issue
    if issue['severity'] == 'High' or issue['severity'] == 'error':
      high_severity_count += 1
    if issue['severity'] == 'Critical':
      critical_severity_count += 1
    if issue['severity'] == 'Medium' or issue['severity'] == 'warning':
      medium_severity_count += 1
    if issue['severity'] == 'Low' or issue['severity'] == 'Information' or issue['severity'] == 'note' or issue['severity'] == 'info':
      low_severity_count += 1


  return {
    'critical': critical_severity_count,
    'high': high_severity_count,
    'medium': medium_severity_count,
    'low': low_severity_count
  }
      
  
def main(occurrence, context):
  full_issues = occurrence['detail']['xissues']
  addDetails = occurrence['detail']['additionalDetails']

  issues = parse_issues(full_issues)
  _summary = to_summary(issues)
  
  return {
    'tag': 'Critical (' + str(_summary['critical']) + '), ' + 'High (' + str(_summary['high']) + ')'
  }