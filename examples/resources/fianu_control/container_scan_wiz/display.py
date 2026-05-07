def main(occurrence, context):
  _result = occurrence['detail']['result']
  _analytics = _result['analytics']

  _vulnerabilities = _analytics['vulnerabilities']

  return {
    'tag': 'Critical (' + str(_vulnerabilities['criticalCount']) + '), ' + 'High (' + str(_vulnerabilities['highCount']) + ')'
  }