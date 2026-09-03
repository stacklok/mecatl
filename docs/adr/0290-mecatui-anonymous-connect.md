# ADR 0290 — Explicit and local-default anonymous mecatui connections

- Status: Accepted
- Date: 2026-09-03
- Scope: `mecatui connect` authentication selection and unauthenticated network deployment posture
- Supersedes: None
- Superseded by: None

## Context

`mecatui connect` treated every missing saved enrollment as a client-side failure, even
when a local `mecated` intentionally required no credential. Login is persistent OIDC
enrollment and must not be overloaded as an anonymous-login operation. At the same time,
ignoring a saved local enrollment would silently discard caller identity, while treating
server TLS as caller authentication hid unauthenticated network exposure.

## Decision

Keep login exclusively for OIDC enrollment. Select connect credentials in this order:
an explicit static token; explicit `--anonymous`; an existing saved enrollment; then a
credential-free connection only for an unenrolled local target (loopback, localhost, IPv6
loopback, or a UNIX socket). An unenrolled remote target still fails with the existing
never-enrolled recovery. A corrupt or unreadable registry never falls back to anonymous.
On a clean local miss, the path uses `OpenExistingRegistry` and `FindTarget`; these may inspect existing registry metadata but never create auth state.

Make `--anonymous` the sole explicit credential-free spelling. It is connect-only, has no
environment equivalent, bypasses saved credentials, and conflicts with both
`--auth-token` and `MECATL_AUTH_TOKEN`. Remove `--no-saved-auth`. Explicit anonymous remote
connections retain target-aware TLS: verified TLS is the default and plaintext requires
an additional `--tls=false` decision.

Classify only bearer authentication, OIDC, and verified client certificates as caller
authentication in server startup posture. Ordinary TLS encrypts the connection and
authenticates the server, but does not identify the caller. Keep a prominent warning for
a non-loopback listener without caller authentication.

## Consequences

Local unauthenticated daemons work without creating login state, while a prior local OIDC
enrollment remains authoritative. Remote anonymous access is possible but always explicit,
and transport downgrade remains a separate explicit act. Private-network deployments may
use network reachability as shared authority, but must document and enforce that boundary;
TLS alone no longer suppresses the warning.

## See also

- [Architecture](../architecture.md)
- [TUI guide](../tui.md)
- [ADR 0287](0287-target-aware-mecatui-tls.md)
- [ADR 0089](0089-cli-clean-break-grammar.md)
- [ADR 0002](0002-documentation-lifecycle.md)
