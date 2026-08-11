---
id: 01-ownership-core
title: Shared verified-owner decision and atomic creation
blocked_by: []
status: done
branch: "plan-caller-separation/01-ownership-core"
worktree: ""
issue: "368"
retries: 1
last_error: ""
accumulator: acc/caller-separation
---

# Shared verified-owner decision and atomic creation

Establish the narrow core/application seam that compares only the verifier-emitted
`(Issuer, Subject)` pair, distinguishes no-principal compatibility from OIDC isolation,
and binds an immutable owner atomically on creation/retry. Keep concrete server and
adapter types out of `engine/agent`; follow ADR-0102.

## Acceptance criteria

- AC1.3: With no verifier wired, no principal is fabricated and the pre-existing ownerless deployment behavior remains available.
  - verify: `TestInvariant_no_fabricated_principal`
- AC1.4: Ownership compares the exact issuer and subject emitted by the verifier; equal subjects from different issuers, alternate issuer spellings, request headers, display names, and grant types cannot collide or select an owner.
  - verify: `TestADR_0102_VerifiedIssuerSubjectPairIsOwnerIdentity`
- AC1.5: Create and retry paths bind the verified owner atomically with visibility. A same-owner retry of the same immutable request is idempotent; a cross-owner ID collision returns absence and cannot overwrite, adopt, or expose the resource.
  - verify: `TestCallerSeparation_Scenario1_AtomicCreationBindsVerifiedOwner`
