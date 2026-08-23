# ADR 0236 — Private HTTPS OIDC issuer admission

- Status: Accepted
- Date: 2026-08-21
- Scope: OIDC issuer discovery and JWKS transport in `authn/oidc`, shared server flags, and the mecak8s Kind fixture
- Superseded by: ADR 0235

## Context

The Kind fixture's Dex issuer is an HTTPS Kubernetes Service. Secure defaults correctly reject its private ClusterIP to prevent discovery or JWKS SSRF, but the former escape hatch combined private-address admission with HTTP issuer acceptance. That combined relaxation cannot safely represent an issuer that is private but still uses TLS and a private CA.

## Decision

Add an explicit private-HTTPS mode. It admits private, loopback, and link-local issuer/JWKS addresses only when a CA bundle path is supplied, while keeping HTTP disabled. It always uses ToolHive's hardened default HTTP client; caller-supplied clients are rejected because they could bypass the default DNS-pinned dial policy.

The mode retains CA and hostname verification, redirect refusal, and per-dial private-address checks. Helm mounts the OIDC CA and passes it directly with `--oidc-ca-cert-file`; it does not alter process-wide trust through `SSL_CERT_FILE`.

Keep `--oidc-insecure-allow-private-issuer` and `InsecureAllowPrivateIssuer` for compatibility, but deprecate them clearly. They remain the only legacy combined HTTP-plus-private-address escape hatch and are prohibited from the Kind mecak8s fixture.

## Consequences

Private HTTPS issuers need an explicit PEM CA file and cannot use a custom production HTTP client. This adds chart validation and a CA projection, but preserves TLS and SSRF defenses for the Kind topology. Existing consumers of the legacy flag retain behavior while receiving a migration path that does not permit HTTP.

## See also

- [Architecture: caller identity](../architecture.md)
- [ADR 0204](./0204-caller-identity-threading.md)
- [ADR 0206](./0206-oidc-authn-module.md)
