---
id: 02-bundle-verifier
title: Canonical SPIFFE bundle and independent verifier
blocked_by: [01-issuer-domain-keyring]
status: pending
branch: ""
worktree: ""
issue: "478"
retries: 0
last_error: ""
accumulator: acc/identity-issuer-substrate
---

# Task brief

Build on task 01’s internal issuer substrate. Add canonical public SPIFFE JWT-bundle projection and a separately configured, bounded HTTPS bundle verifier returning typed verified identity. Keep it root-internal; do not add generic claim authorization, compatibility JWKS, vMCP, ToolHive, a network mint API, or B0 Redis state.

## Acceptance criteria

- AC3.1: A separately configured verifier accepts an issuer JWT-SVID only for the configured trust domain, valid SPIFFE subject, one expected audience, active `kid`, ES256 signature, and valid bounded times.
  - verify: `TestIdentityIssuerSubstrate_Scenario3_IndependentBundleVerification`
- AC3.2: Algorithm confusion, unknown/duplicate `kid`, bundle substitution/regression, foreign subject, wrong or multiple audience, malformed/oversized token, expired/future/excessive-lifetime token all fail closed.
  - verify: `TestADR_0300_VerifierFailsClosed`
- AC3.3: A verifier retains a complete bundle only through `R`; failed refresh before then preserves it, while expiry of `R` denies verification and marks readiness unready.
  - verify: `TestInvariant_identity_bundle_freshness_bound`
- AC3.4: The bundle projects only public P-256 JWKs, monotonic sequence, and advisory refresh hint; no private material or compatibility JWKS endpoint is emitted.
  - verify: `TestADR_0300_CanonicalBundleHasNoPrivateMaterial`
