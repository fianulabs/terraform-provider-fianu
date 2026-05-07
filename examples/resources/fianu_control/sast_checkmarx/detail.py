def parse_issue(issue):
  return {
    'vulnerability': issue['vulnerability'],
    'severity': issue['severity'],
    'url': issue['link'],
    'categories': issue['additionalDetails']['categories'].split(','),
    'fix': issue['additionalDetails']['recommendedFix'],
    'cve': issue['cve'],
    'osa': issue['osaDetails']
  }

def get_result_rule(id, rules):
  for rule in rules:
    if rule['id'] == id:
      return rule

  return {}
  
def parse_result_to_issue(result, tool):
  _rules = tool['driver']['rules']
  _rule = get_result_rule(result['ruleId'], _rules)
  
  return {
    'fix': result['message']['text'],
    'priority': result['properties']['priorityScore'],
    'vulnerability': result['ruleId'],
    'categories': _rule['properties']['categories'],
    'tags': _rule['properties']['tags'],
    'severity': result['level'],
    'cwe': _rule['properties']['cwe']
  }

def parse_issues(issues):
  _issues = []
  
  for issue in issues:
    _issues.append(parse_issue(issue)) 

  return _issues

def parse_results(results, tool):
  _issues = []

  for result in results:
    _issues.append(parse_result_to_issue(result, tool))

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
  detail = occurrence.get('detail', {})
  full_issues = detail.get('xissues', [])
  addDetails = detail.get('additionalDetails', {})

  if not full_issues or not addDetails:
    return {
      'id': '',
      'risk': '',
      'severity': '',
      'timestamp': '',
      'summary': {'critical': 0, 'high': 0, 'medium': 0, 'low': 0},
      'vulnerabilities': []
    }

  issues = parse_issues(full_issues)

  return {
    'id': addDetails.get('scanId', ''),
    'risk': addDetails.get('scanRisk', ''),
    'severity': addDetails.get('scanRiskSeverity', ''),
    'timestamp': addDetails.get('scanStartDate', ''),
    'summary': to_summary(issues),
    'vulnerabilities': issues
  }