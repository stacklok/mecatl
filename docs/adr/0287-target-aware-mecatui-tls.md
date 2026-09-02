# ADR 0287 — Target-aware mecatui remote TLS defaults

- Status: Accepted
- Date: 2026-09-02
- Scope: `mecatui connect` transport and saved remote authentication
- Supersedes: None
- Superseded by: None

## Context

A Boolean TLS flag could not distinguish omission from `--tls=false`. That made a
safe remote default impossible while preserving convenient loopback plaintext.
Saved OIDC credentials also need a stronger guarantee: a bearer-bearing managed
connection must not silently use plaintext merely because its target is loopback.
The issuer's private CA is not necessarily the gRPC server's CA.

## Decision

Resolve TLS after the connect address is known. Omitted `--tls` selects verified
TLS for non-loopback or unparseable targets and plaintext for loopback. Explicit
`--tls=false` is the sole plaintext downgrade; TLS CA and insecure-verification
options imply TLS and contradict it. Require an explicit internal authorization
before the client dials remote plaintext, while continuing to reject a bearer on
that transport. Refuse a bearer on unverified TLS to a non-local target for the
same reason: that transport is encrypted but unauthenticated, so any MITM
certificate reads the token, and `RequireTransportSecurity` cannot tell verified
from unverified TLS. Both bearer refusals live in `Dial`, so every caller of the
client package inherits them rather than each transport policy re-deriving them.

One predicate classifies a target, `client.IsLocalTarget` (loopback host:port, or
a `unix://` socket the filesystem protects rather than the network). The connect
TLS policy, both pre-dial plaintext guards, and the per-RPC bearer credential's
`RequireTransportSecurity` all read it. A target any one of them treats as local
must be local to all of them: a policy layer that defaults a `unix://` socket to
TLS would hand it a handshake against a plaintext socket, and a guard that admits
a target the credential later rejects replaces this ADR's actionable error with
gRPC's opaque `credentials require transport level security`.

A saved remote-auth registry hit always selects verified gRPC TLS and rejects an
explicit plaintext downgrade or insecure verification. Keep the saved issuer CA
for OIDC issuer operations only; `--tls-ca` is the only gRPC server trust input.

## Consequences

Ordinary remote connections become secure without requiring a redundant flag,
while local loopback workflows remain simple. Intentional remote plaintext stays
available only to controlled, unauthenticated testing callers — `e2e/harness`
sets that internal authorization for `MECATL_E2E_TARGET`, whose transport the
operator has already chosen. Supplying both `--tls-ca` and `--insecure` is now
rejected rather than silently resolving to skipped verification. Saved credentials
cannot be used with a development plaintext server; operators must configure a
verified server endpoint instead.

## See also

- [Architecture](../architecture.md)
- [TUI guide](../tui.md)
- [ADR 0277](0277-remote-mecatui-oidc.md)
- [ADR 0002](0002-documentation-lifecycle.md)
