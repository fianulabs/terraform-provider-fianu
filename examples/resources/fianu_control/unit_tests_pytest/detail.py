def parse_tests_from_suites(tests):
  _tests = []
  _totals = {
    'error': 0,
    'failed': 0,
    'passed': 0,
    'skipped': 0
  }
  for test in tests:
    try:
      outcome = test.get("outcome", "unknown")
      if outcome not in _totals:
        _totals[outcome] = 0
      _totals[outcome] += 1
      _tests.append(test)
    except:
      pass
  return _tests, _totals


def main(occurrence, context):
  _testsuites = occurrence['detail'].get("tests", [])
  _tests, _summary = parse_tests_from_suites(_testsuites)
  return {
    'summary': _summary,
    'tests': _tests
  }
