---
id: 01-logical-identity-value
title: Canonical logical-definition identity and closed claim value
blocked_by: []
status: pending
branch: ""
worktree: ""
issue: "377"
retries: 0
last_error: ""
accumulator: acc/logical-agent-identity-projection
---

# Task brief

Create the root-internal typed logical-agent identity/profile value and its validation boundary. Keep it independent of agent-loop, session, composition, RPC, and persistence wiring. Use the ADR-0252 canonical SPIFFE subject format: five closed tiers, exact UTF-8 name, display-only slug, exact length-delimited SHA-256 input, RFC 4648 lowercase unpadded Base32 digest, SPIFFE grammar, and 2048-byte cap. Define the closed v1 claim value and bounds, including optional audit-only instance and canonical exact tools. Reject unknown schema members, duplicates, noncanonical tools, control characters, invalid UTF-8, and invalid bounds. Do not introduce token signing or a second authority policy in this task.

## Acceptance criteria

- AC1.1: Each of `system`, `managed`, `driver`, `user`, and `project` produces the versioned local subject `/mecatl/agent-definition/v1/<tier>/<slug>--<digest>`, where the digest golden is all SHA-256 bits encoded as RFC 4648 lowercase Base32 without padding over the ADR's byte-exact tuple.
  - verify: `TestADR_0252_CanonicalLogicalAgentSubject`
- AC1.2: Distinct exact names that share a display slug, the same name in different tiers, case variants, and canonically equivalent but byte-distinct Unicode names produce distinct subjects; rename is a principal change.
  - verify: `TestLogicalAgentIdentityProjection_Scenario1_DefinitionIdentityDoesNotCollapse`
- AC1.3: Empty, invalid-UTF-8, control-bearing, oversized, unknown-tier, malformed-path, percent-encoded, foreign-trust-domain, query/fragment, and over-2048-byte subjects fail without returning a partial identity.
  - verify: `TestADR_0252_SubjectAndDefinitionBoundsFailClosed`
- AC1.4: The v1 claim requires tier, exact name, and tools; optional instance is omitted rather than `null`; duplicate requested tools and every approved field/count/byte-bound violation fail validation before signing, while a canonical unique positive control succeeds.
  - verify: `TestLogicalAgentIdentityProjection_Scenario1_ClosedBoundedValue`
