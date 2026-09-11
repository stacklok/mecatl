# ADR 0327 — Persist saved mecatui gRPC server CA references

- Status: Accepted
- Date: 2026-09-08
- Scope: `mecatui login` connection metadata and saved-target gRPC TLS trust
- Supersedes: only ADR 0287's sole custom gRPC server-CA-input clause; all other ADR 0287 decisions remain authoritative
- Superseded by: none

## Context

ADR 0287 separated OIDC issuer trust from gRPC server trust and made `connect --tls-ca` the only custom gRPC trust input. That protects against accidentally reusing an issuer CA for a server, but it requires an operator to repeat the server CA on every connect even though mecatui already persists target-specific connection metadata.

Private deployments commonly use distinct private CAs for the issuer and gRPC server. A saved target must retain that distinction, must not store certificate contents, and must not let persisted state override an explicit connect-time choice.

## Decision

Supersede only ADR 0287's rule that `connect --tls-ca` is the sole custom gRPC server-trust input. Retain its target-aware TLS defaults, verified-TLS requirement for saved authentication, issuer/server trust separation, plaintext restrictions, and insecure-verification restrictions.

Add `mecatui login --server-tls-ca PATH` as an optional target-specific gRPC server trust input. Resolve the path to an absolute clean path before enrollment and persist that reference as `server_ca_file`; never persist CA contents. Protected-resource-discovery login carries the same input into the confirmed enrollment.

On a later saved-target connect, use the persisted server CA by default. A non-empty explicit `connect --tls-ca PATH` overrides it for that invocation. The login `--tls-ca` remains exclusively issuer trust and is never copied into the gRPC dial configuration.

Treat persisted `server_ca_file` as validated registry metadata: malformed or non-absolute paths quarantine only their row, while new registry writes reject them. Legacy rows without the field continue to use system roots.

## Consequences

Operators can enroll and reconnect to a private-CA gRPC server without repeating its CA path, while issuer trust and server trust remain independently configured. Moving or deleting the referenced file makes the saved connection unusable until the path is restored, the connection is re-enrolled, or an explicit connect-time CA is supplied.

Persisting a local path makes saved connection metadata machine-specific, as issuer CA references already are. Explicit connect-time input remains the highest-priority trust choice and does not mutate the saved row.

## See also

- [ADR 0287 — Target-aware mecatui remote TLS defaults](./0287-target-aware-mecatui-tls.md)
- [ADR 0305 — OAuth protected-resource discovery](./0305-oauth-protected-resource-discovery.md)
- [Architecture](../architecture.md)
- [TUI guide](../tui.md)
- [Documentation lifecycle](./0002-documentation-lifecycle.md)
