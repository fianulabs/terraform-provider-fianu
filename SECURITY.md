# Security Policy

## Reporting a vulnerability

If you believe you've found a security vulnerability in
`terraform-provider-fianu`, please report it privately rather than opening a
public GitHub issue.

**Preferred:** Open a [private security advisory](https://github.com/fianulabs/terraform-provider-fianu/security/advisories/new)
on this repository. GitHub will route it to the maintainers without making it
public.

**Alternative:** Email `security@fianu.io` with:

- A description of the issue and its impact
- Steps to reproduce (HCL snippet, provider version, Terraform version)
- Any proof-of-concept code or logs (please redact secrets)

We aim to acknowledge reports within 3 business days and to provide a status
update within 7 business days.

## Scope

This policy covers the `terraform-provider-fianu` codebase and its release
artifacts (binaries, signatures, manifest). Vulnerabilities in the Fianu
Console backend or the broader `fianulabs/core` codebase should be reported
to `security@fianu.io` directly.

## Supported versions

During v0.x, only the latest minor release receives security fixes. Once v1.0
ships, we will document a support window here.

## Disclosure

We follow coordinated disclosure: once a fix is released, we will publish a
GitHub Security Advisory crediting the reporter (if they wish) and request a
CVE where applicable.
