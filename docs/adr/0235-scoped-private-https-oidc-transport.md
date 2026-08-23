# ADR 0235 — Scoped private HTTPS OIDC transport

- Status: Accepted
- Date: 2026-08-21
- Scope: private HTTPS OIDC discovery and JWKS transport
- Supersedes: ADR 0234

## Context

ADR 0234 separated HTTP admission from private-address admission, but mapping the latter directly to ToolHive's broad `AllowPrivateIP` policy would allow any private discovery-provided JWKS endpoint and would remove its per-dial guard. That is an SSRF regression.

## Decision

Private HTTPS mode uses an internally constructed scoped transport rather than ToolHive's broad private-IP option. At construction it resolves only the configured HTTPS issuer and optional explicit JWKS hosts, records their private addresses, and accepts no other host or port. Every dial re-resolves the approved hostname and connects only to the intersection with the recorded private addresses. Keep-alives are disabled so every request takes that check. Redirects are refused.

The scoped transport loads the explicitly configured CA bundle and leaves Go's TLS hostname verification enabled. Caller-supplied HTTP clients remain rejected in this mode. A discovery document may name a JWKS URL only on an already configured host and port; an arbitrary private `jwks_uri` fails closed.

## Consequences

The Kind issuer remains supported without global private-network access. DNS changes fail closed until the deployment is restarted with the intended address set. Operators needing a distinct JWKS host must configure its URL explicitly. The legacy combined HTTP/private escape hatch remains unchanged and deprecated.

## See also

- [Architecture: caller identity](../architecture.md)
- [ADR 0236](./0236-private-https-oidc-issuer.md)
- [ADR 0206](./0206-oidc-authn-module.md)
