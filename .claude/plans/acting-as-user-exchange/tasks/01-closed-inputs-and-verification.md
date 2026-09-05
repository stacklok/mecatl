---
id: 01-closed-inputs-and-verification
title: Closed exchange values and subject/I2 verification
blocked_by: []
status: done
branch: "plan-acting-as-user-exchange/01-closed-inputs-and-verification"
worktree: ""
issue: "372"
retries: 0
last_error: ""
accumulator: acc/acting-as-user-exchange
---

# Task brief

Create the root-internal `internal/actingaccess` closed value and verification substrate. Keep the package adapter-neutral and use the existing `internal/identityissuer` logical-agent verifier rather than duplicating JWT verification. Implement opaque, non-formatting/non-marshaling secret wrappers; canonical owner/presenter/registered target values; a closed subject-assertion verifier port/result; and I2 verification that copies authority. Do not build ToolHive transport, sidecar, cache, or any production exchange client.

## Acceptance criteria

- AC1.1: An acting-access request accepts only bounded, canonical owner, presenter, resource, operation, and sorted unique scope values; empty, duplicate, control-bearing, or unknown values are refused.
  - verify: `TestActingAccess_Scenario1_RejectsNonCanonicalRequest`
- AC1.2: The public I3-C API exposes no generic claims map, arbitrary URL/audience, backend credential, authorization header, session store, event log, or ToolHive type.
  - verify: `TestInvariant_acting_access_closed_inputs`
- AC1.3: Subject assertion, I2 token, and output token are distinct non-serializable/redacted secret types and cannot be substituted for one another.
  - verify: `TestActingAccess_Scenario1_SeparatesCredentialProfiles`
- AC2.1: A valid exchange-subject assertion whose issuer-qualified identity matches the durable owner reaches the policy gate; changing only issuer or subject refuses before issuance. Wrong/multiple audience, login-bearer or ID-token substitution, wrong `typ`/authorized party, missing/excessive-age temporal fields, duplicate registered claims, wrong algorithm, and oversized input also refuse.
  - verify: `TestADR_0302_ClosedSubjectAssertionProfile`
- AC2.2: A valid compact I2 JWT produces a copied verified logical actor, while wrong audience, expired, malformed, oversized, duplicate-claim, stale-bundle, and algorithm-confused actor tokens refuse without partial facts. Rotation accepts old/new keys only during ADR-0300 overlap and refuses retired or refresh-stale bundles.
  - verify: `TestADR_0302_ActorProfileVerificationAndRotation`
- AC2.3: A caller-constructed or post-verification-mutated logical-agent value cannot widen the tools used by the gate.
  - verify: `TestInvariant_acting_access_verified_actor_copy`
