---
id: 03-keycloak-optional
title: "Optional Keycloak identity overlay"
blocked_by: [01-kind-base]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecak8s-kind-fixture
---

# Optional Keycloak identity overlay

Move the Keycloak, certificate, and realm assets into the new base fixture as
an independently idempotent opt-in layer. Add `mecak8s:kind-keycloak-apply`
and `kind-keycloak-setup`. Split the chart overlay so the OIDC audience is the
mecak8s resource identifier, not vMCP. The public `mecatui-kind` client uses
PKCE and optional `mecak8s:access`; mecak8s itself is the resource server, not
a Keycloak OAuth client.

## Acceptance criteria

- AC3.1: `mecak8s:kind-setup` neither invokes nor transitively depends on
  cert-manager, certificates, Keycloak, or OIDC/TLS Helm flags; the separately
  idempotent `kind-keycloak-apply` and composed `kind-keycloak-setup` own those
  effects, and a failed apply does not report a ready authenticated fixture.
  - verify: `TestMecak8sKindFixture_Scenario3_KeycloakIsOptIn`
- AC3.2: The Keycloak overlay projects exact TLS and OIDC-CA Secret keys and
  configures the exact HTTPS issuer and `oidc.audience: mecak8s`. It uses
  `--oidc-ca-cert-file` and the disposable-only
  `--oidc-allow-private-https-issuer`, never `SSL_CERT_FILE` or the deprecated
  HTTP/private-issuer relaxation.
  - verify: `TestMecak8sKindFixture_Scenario3_KeycloakOIDCOverlay`
- AC3.3: `mecatui-kind` remains a public authorization-code + PKCE S256 client
  with no client secret, implicit flow, direct grants, or service accounts. Its
  optional `mecak8s:access` scope adds `mecak8s` only to the requested access
  token's audience, never to an ID token or an access token that omits the
  scope.
  - verify: `TestMecak8sKindFixture_Scenario3_ResourceAudience`
