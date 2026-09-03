# ADR 0290 — Server-authoritative credential-free mecatui connections

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
credential-free connection for any clean enrollment miss. Target classification does not
decide whether caller authentication is required: the server does, by accepting the RPC or
returning `Unauthenticated`. A corrupt or unreadable registry, keyring, or credential record
never falls back to credential-free operation. On a clean miss, the path uses
`OpenExistingRegistry` and `FindTarget`; these may inspect existing registry metadata but
never create auth state.

Make `--anonymous` the sole explicit credential-free spelling. It is connect-only, has no
environment equivalent, and bypasses saved credentials when no static token is selected. An
explicit `--auth-token` or `MECATL_AUTH_TOKEN` has higher priority and therefore wins if both
are present. Remove `--no-saved-auth`. Explicit anonymous remote
connections retain target-aware TLS: verified TLS is the default and plaintext requires
an additional `--tls=false` decision. When a credential-free RPC actually returns
`Unauthenticated`, report that the server requires caller authentication and recommend
`--auth-token` or `mecatui login ADDRESS`, explicitly naming login as OIDC enrollment. If
`--anonymous` caused the bypass, say so. `PermissionDenied` remains authorization and
network/TLS failures never acquire login recovery.

Classify only bearer authentication, OIDC, and verified client certificates as caller
authentication in server startup posture. Ordinary TLS encrypts the connection and
authenticates the server, but does not identify the caller. Keep a prominent warning for
a non-loopback listener without caller authentication.

## Consequences

Credential-free daemons work without creating login state, while a prior OIDC enrollment
remains authoritative. `--anonymous` is the deliberate override that bypasses such saved
state. Remote clean misses are also tried credential-free, but still default to verified TLS;
plaintext remains a separate explicit act. Private-network deployments such as Tailscale may
use network reachability and ACLs as shared authority, but that means every admitted peer is
effectively one shared caller unless the server enables caller authentication. The client
must not infer either authority or transport trust from a private IP, DNS name, or
Tailscale-like target; TLS alone does not authenticate the caller.

## See also

- [Architecture](../architecture.md)
- [TUI guide](../tui.md)
- [ADR 0287](0287-target-aware-mecatui-tls.md)
- [ADR 0089](0089-cli-clean-break-grammar.md)
- [ADR 0002](0002-documentation-lifecycle.md)
