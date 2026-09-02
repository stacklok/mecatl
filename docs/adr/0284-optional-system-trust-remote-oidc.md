# ADR 0284 — Optional system trust for remote mecatui OIDC issuers

- Status: Accepted
- Date: 2026-09-02
- Scope: `mecatui` remote OIDC issuer trust during enrollment, refresh, reauthentication, and logout
- Supersedes: ADR 0277's mandatory issuer-CA requirement only

## Context

ADR 0277 required an issuer CA bundle for every `mecatui login` invocation.
That contradicts the flag's private-issuer wording and prevents normal public
OIDC issuers from using the host system trust store. The saved issuer CA path is
also consumed by enrollment, refresh, reauthentication, and logout, so changing
only the initial command would create credentials that fail in later processes.

## Decision

Make `mecatui login --tls-ca` optional. When absent, issuer discovery, token
exchange, JWKS validation, refresh, and revocation use the system trust store
with HTTPS and redirect refusal. When supplied, retain the existing private
HTTPS transport and its explicit CA roots, private-address admission, DNS-pinned
dials, hostname verification, and redirect refusal.

An issuer CA remains distinct from `mecatui connect --tls-ca`, which verifies the
remote gRPC server. Neither trust root is reused for the other endpoint.

Issuer, client ID, and audience remain explicit deployment-supplied login
configuration. This decision adds no server discovery endpoint and does not
infer OAuth client registration metadata from the target address.

## Consequences

Public OIDC issuers no longer require an unnecessary local system-CA file path.
Private issuers still require an explicit local bundle; no CA material is
obtained from an untrusted endpoint. Empty saved issuer-CA metadata now
intentionally means system-root verification, so every saved-credential path
must preserve that distinction.

## See also

- [ADR 0277 — Remote mecatui OIDC client authentication](./0277-remote-mecatui-oidc.md)
- [ADR 0235 — Scoped private HTTPS OIDC transport](./0235-scoped-private-https-oidc-transport.md)
- [TUI guide](../tui.md)
- [Architecture guide](../architecture.md)
