---
id: 01-issuer-domain-keyring
title: Trust domain, immutable keyring, and bounded issuer envelope
blocked_by: []
status: pending
branch: ""
worktree: ""
issue: "478"
retries: 0
last_error: ""
accumulator: acc/identity-issuer-substrate
---

# Task brief

Implement the root-internal I1 domain/keyring substrate, strictly outside `engine/`: explicit SPIFFE trust-domain validation; bounded immutable manifest plus PKCS#8 P-256 loading; deterministic RFC-7638-thumbprint `kid`; no partial publication or ephemeral fallback; and typed bounded `IssueJWTSubject`. Keep arbitrary signing, claims maps, network mint endpoints, Kubernetes loading, and broker lifecycle out of this task.

## Acceptance criteria

- AC1.2: An enabled host rejects absent/invalid trust domain, invalid `T`/`S`/`R`, unregistered audience, or incomplete HTTPS bootstrap before key loading.
  - verify: `TestADR_0300_IdentityConfigFailsClosed`
- AC1.3: A configured trust domain is validated by SPIFFE grammar and is never inferred from request host, callback origin, bundle URL, or Kubernetes metadata.
  - verify: `TestInvariant_identity_trust_domain_explicit`
- AC2.1: A strict manifest plus matching PKCS#8 P-256 key items produces a deterministic public-JWK-thumbprint `kid` and one active ES256 signer.
  - verify: `TestIdentityIssuerSubstrate_Scenario2_LoadImmutableKeyring`
- AC2.2: Malformed, oversized, partial, duplicate, unknown, non-P-256, mismatched, or no-active-signer keyrings fail without publishing partial state or generating an ephemeral replacement.
  - verify: `TestADR_0300_KeyringRejectsInvalidGeneration`
- AC2.3: `IssueJWTSubject` accepts only a local-trust-domain SPIFFE subject, one registered audience, and TTL within `T`; caller-controlled JOSE headers, claims, keys, timestamps, and arbitrary signing input are unavailable.
  - verify: `TestInvariant_identity_issuer_typed_envelope`
