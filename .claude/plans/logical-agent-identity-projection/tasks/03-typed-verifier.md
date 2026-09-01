---
id: 03-typed-verifier
title: Single-pass typed logical-agent verifier
blocked_by: [02-constrained-issuer]
status: in-progress
branch: ""
worktree: ""
issue: "478"
retries: 0
last_error: ""
accumulator: acc/logical-agent-identity-projection
---

# Task brief

**Worker gate scope:** run only focused tests for packages/files you change and any directly dependent focused tests. Do **not** run `task lint`, `task test`, `task docs`, or other repository-wide gates; the orchestrator runs those once after all tasks merge.

Implement the I2-specific independent verifier as a mutually exclusive profile beside, not a weakening of, I1's registered-claims-only canary verifier. It must validate header, envelope, signature, bundle/key selection, and typed profile through one security representation, rejecting duplicate JSON names at every security-relevant level. Return only the approved typed verified identity with defensive copies; never raw compact JWT, generic claims, partial result, or authorization decision. Retain I1's fixed ES256, local trust-domain, singleton-audience, time/bundle freshness semantics and validate I2 `jti`, canonical subject, closed fields, and canonical tools.

## Acceptance criteria

- AC4.1: An independently configured verifier accepts a valid I2 token and returns exactly trust domain, recomputed subject, tier, exact name, optional instance, a fresh sorted unique tool slice, JWT ID, and expiry—without raw compact JWT, generic claims, or a policy verdict.
  - verify: `TestLogicalAgentIdentityProjection_Scenario4_IndependentTypedVerification`
- AC4.2: Payload tampering, attacker-key signing with a copied `kid`, wrong algorithm/key/use/trust domain/audience/time, unknown key, unavailable/stale/incomplete/regressed bundle, missing/empty/malformed/padded/wrong-length/non-base64url `jti`, missing/malformed v1 claim, unknown tier/version/member, multiple logical-agent versions, non-canonical tools, or subject/profile disagreement fails with a zero typed result and no partial authority.
  - verify: `TestADR_0252_TypedVerifierFailsClosed`
- AC4.3: Duplicate protected-header, registered-claim, top-level profile, and every v1-object member fail even when a last-value-wins parser would see a valid final value; a canonical positive-control sibling verifies. The production verifier constructs its typed result from the signature-validated typed claims representation and does not decode the payload again through an unverified path.
  - verify: `TestADR_0252_TypedVerifierUsesOneSecurityRepresentation`
- AC4.4: A valid I1 canary token fails I2 verification, an I2 token remains rejected by I1's registered-claims-only canary verifier, and generic unrelated JWT claims never grant tools or select another validation profile.
  - verify: `TestADR_0252_ProfileConfusionMatrix`
