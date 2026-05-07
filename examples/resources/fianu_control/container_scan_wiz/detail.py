def parse_vulnerabilities_from_packages(os_packages, cpe_packages, lib_packages):
  _vulns = []
  _failed_policies = {}

  if lib_packages is not None:
    for pkg in lib_packages:
      try:
          for vuln in pkg['vulnerabilities']:
              vuln['package'] = pkg['name']
              _vulns.append(vuln)

          for fp in pkg['failedPolicyMatches']:
              fp_id = fp['policy']['id']
              _failed_policies[fp_id] = fp
      except:
          pass

  if cpe_packages is not None:
    for pkg in cpe_packages:
      try:
          for vuln in pkg['vulnerabilities']:
              vuln['package'] = pkg['name']
              _vulns.append(vuln)

          for fp in pkg['failedPolicyMatches']:
              fp_id = fp['policy']['id']
              _failed_policies[fp_id] = fp
      except:
          pass

  if os_packages is not None:
    for pkg in os_packages:
        try:
            for vuln in pkg['vulnerabilities']:
                vuln['package'] = pkg['name']
                _vulns.append(vuln)

            for fp in pkg['failedPolicyMatches']:
                fp_id = fp['policy']['id']
                _failed_policies[fp_id] = fp
        except:
            pass

  return _vulns, list(_failed_policies.values())

def main(occurrence, context):
  _result = occurrence['detail']['result']

  _analytics = _result['analytics']
  _os_packages = _result['osPackages']
  _cpe_packages = _result['cpes']
  _lib_packages = _result['libraries']

  _vulnerabilities, _failed_policies = parse_vulnerabilities_from_packages(_os_packages, _cpe_packages, _lib_packages)

  return {
    'summary': _analytics,
    'vulnerabilities': _vulnerabilities,
    'failedPolicies': _failed_policies
  }