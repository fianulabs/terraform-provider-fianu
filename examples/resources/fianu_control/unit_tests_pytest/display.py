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
  _detail = occurrence['detail']

  _testsuites = _detail.get("tests", [])
  _tests, _summary = parse_tests_from_suites(_testsuites)

  _passed = _summary.get("passed", 0)
  _failed = _summary.get("failed", 0)
  _skipped = _summary.get("skipped", 0)

  return {'tag': 'Passed ({0}), Failed ({1}), Skipped ({2})'.format(str(_passed), str(_failed), str(_skipped))}
