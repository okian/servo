# Security policy

## Supported versions

Only the latest `v3` release is supported. `v3` is a from-scratch rewrite that shares no API with
what was published under `github.com/okian/servo` or `github.com/okian/servo/v2` (see
[CHANGELOG.md](CHANGELOG.md)); those import paths do not receive fixes.

## Reporting a vulnerability

Report suspected vulnerabilities privately through GitHub, not as a public issue:

**[github.com/okian/servo/security/advisories/new](https://github.com/okian/servo/security/advisories/new)**

This reaches maintainers only and lets us coordinate a fix before any public disclosure. Include
what you'd include in a bug report: affected version, a reproduction (a minimal spec file or
`servo generate`/`servo check` invocation is usually enough), and the impact as you see it.

This is a single-maintainer project run on a best-effort basis — expect an initial response within
14 days, not a guaranteed SLA. Please don't disclose publicly until a fix has shipped.

## Scope

In scope: `servo`, `servotest`, `cmd/servo`, `cmd/servo-vet`, and the code they generate.
Out of scope: `examples/` and `docs/tutorial/` are demonstration code, not deployed services.
