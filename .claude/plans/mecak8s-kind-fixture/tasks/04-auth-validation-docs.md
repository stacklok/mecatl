---
id: 04-auth-validation-docs
title: "Keycloak journey validation and documentation"
blocked_by: [02-provider-mode, 03-keycloak-optional]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecak8s-kind-fixture
---

# Keycloak journey validation and documentation

Complete the optional Keycloak layer with loopback-only host access and
fail-closed OIDC evidence. Update the fixture, operator, deployment, and
public documentation without duplicating the chart contract. Keep vMCP,
ToolHive, RFC 8693, and the ToolHive CA workaround outside this task.

## Acceptance criteria

- AC3.4: A local client reaches the authenticated mecak8s service only through
  an explicit loopback-only port-forward and certificate-covered hostname; the
  base fixture remains ClusterIP with no externally reachable Service,
  LoadBalancer, Ingress, or wildcard host binding.
  - verify: `TestMecak8sKindFixture_Scenario3_LoopbackReachability`
- AC3.5: mecak8s rejects an absent, forged, wrong-issuer, wrong-audience,
  wrong-hostname, or untrusted-CA bearer request before authenticated API
  handling, and accepts a valid Keycloak access token whose audience contains
  `mecak8s`.
  - verify: `TestMecak8sKindFixture_Scenario3_AuthenticatedRequest`
- AC3.6: An OIDC-enabled deployment fails closed when its initial issuer/JWKS
  fetch is unavailable; after a successful fetch, an outage beyond the
  configured JWKS staleness bound yields retryable 503 rather than an
  unauthenticated fallback or a 401 token classification.
  - verify: `TestADR_0205_KeycloakFixtureJWKSStaleness`
- AC3.7: Fixture instructions document authorization-code + PKCE as the normal
  login journey; any fixture password grant is identified as a narrowly scoped
  test helper and is not presented as the normal client flow.
  - verify: `TestMecak8sKindFixture_Scenario3_LoginDocumentation`
