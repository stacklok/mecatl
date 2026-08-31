# ADR 0257 — Remote logout provider budget

- Status: Accepted
- Date: 2026-08-28
- Scope: Remote mecatui OIDC logout provider communication
- Supersedes: ADR 0260 (logout provider budget only)
- Superseded by: None

## Context

ADR 0260 set a five-second operation-wide budget for best-effort issuer discovery
and RFC 7009 revocation after local logout. On macOS, a Kubernetes
`cluster.local` hostname can spend about five seconds in mDNS before falling
back to `/etc/hosts`. The scoped private-HTTPS client resolves the hostname at
endpoint admission and again before its first dial, so the prior budget can
expire before TLS, discovery, or revocation begins.

## Decision

Use one operation-wide fifteen-second budget, starting before scoped HTTP-client
construction and still shared by discovery and every retained-token revocation.
A shorter caller deadline continues to win. Reuse the scoped client during the
operation so later discovery and revocation requests do not repeat its
first-dial DNS validation unnecessarily.

The timeout remains a provider-cleanup bound only: local credential and registry
removal finish first, and any provider failure is reported as a secret-free
outcome without restoring local state.

## Consequences

Logout remains bounded and idempotent while reliably reaching provider cleanup
for the documented Kind host alias on macOS. In the failure case, the operator
may wait up to fifteen seconds rather than five; local logout still completes
without waiting for a provider success.

## See also

- [ADR 0260 — Remote mecatui OIDC client authentication](./0260-remote-mecatui-oidc.md)
- `internal/adapter/clientauth/logout.go` (`revocationTimeout`)
- [TUI guide](../tui.md)
- [Architecture guide](../architecture.md)

---
