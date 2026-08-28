# ADR 0247 — Safe target logout for remote mecatui OIDC

- Status: Accepted
- Date: 2026-08-27
- Scope: remote mecatui target registry deletion, credential cleanup, and provider revocation
- Supersedes: None
- Superseded by: ADR 0244

## Context

Remote enrollment has two durable halves: public target metadata in the connection
registry and secret OAuth material in a keyring-wrapped encrypted store keyed by the
complete identity. Re-enrollment can replace both, but operators had no deliberate
removal path. A missing half is already possible after expiry cleanup, and a credential
rotation can race any attempted delete. Removing metadata before resolving the
credential would turn a recoverable partial state into an unreachable refresh-token
orphan.

OAuth 2.0 token revocation is also not universally available. Treating an unavailable
issuer as a reason to retain local credentials would make logout least reliable when it
is most needed, while deleting only locally can leave provider-side tokens valid.

## Decision

Add `mecatui logout ADDRESS` and key registry deletion by canonical target. Logout
loads every registry identity for that target, then loads and conditionally deletes each
credential using its opaque CAS version. On a CAS conflict it reloads the current record
once and retries, so a concurrent refresh can still be removed safely. It deletes the
registry snapshot only after all credentials are deleted or already absent. Registry deletion also compares the expected
target entries, so a concurrent re-enrollment is retained. A credential CAS conflict,
unreadable credential, unavailable store, or registry race is reported as incomplete
and retains metadata rather than creating a new unreachable orphan. Repeating logout
for an absent target succeeds and reports that nothing was saved.

Open the credential store through non-creating keyring access. Per-target logout never
creates or deletes the shared encryption key.

After local credential and registry cleanup, make a bounded best-effort RFC 7009 attempt against
the issuer-advertised, same-origin HTTPS revocation endpoint. Send the public client ID
without a client secret; revoke the refresh token and access token when present. Discovery,
transport, or provider failure never blocks the subsequent local CAS delete, and output
contains only target/identity metadata and secret-free status.

A credential with no registry entry remains outside the operation's reach. The store has
no enumeration capability and its filename is not a reversible identity index. This
command therefore does not claim to prune pre-existing orphans; adding enumeration or an
explicit prune operation is a separate decision.

## Consequences

Operators gain an idempotent repair/removal command, including duplicate target entries,
without weakening concurrent token rotation. Some partial states intentionally require a
retry: preserving metadata is safer than claiming logout while making a live credential
unreachable. Provider-side termination is best effort rather than guaranteed, and the
command says so. Existing credential orphans remain until the store gains a safe
enumeration/pruning contract.

## See also

- [Remote mecatui OIDC client login](./0248-remote-mecatui-oidc-login.md)
- [TUI transport and login](../tui.md)
- [Architecture](../architecture.md)
- `user-docs/mecatui/remote-servers.md`
