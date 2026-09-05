---
id: 04-rotation-and-leakage
title: Rotation-safe remint and credential-leak proofs
blocked_by: [03-typed-verifier]
status: done
branch: "plan-logical-agent-identity-projection/04-rotation-and-leakage"
worktree: ""
issue: "375"
retries: 0
last_error: ""
accumulator: acc/logical-agent-identity-projection
---

# Task brief

**Worker gate scope:** run only focused tests for packages/files you change and any directly dependent focused tests. Do **not** run `task lint`, `task test`, `task docs`, or other repository-wide gates; the orchestrator runs those once after all tasks merge.

Extend I2 tests over the existing I1 rotation substrate. Prove remint derives fresh ephemeral credentials from the same typed logical identity and same-or-narrower tool authority, without persisting or exposing a compact token. Keep this pure I2 issue/verify work: do not add B4 spawn, resume, session, event-log, or broker wiring. Exercise a real minted token and deliberately malicious claim/error inputs so absence checks are not vacuous.

## Acceptance criteria

- AC5.1: Before and after key activation, independently verified tokens have the same canonical subject/tier/name/instance and same-or-narrower tools, but fresh `jti`, signature, issuance time, and the active `kid`.
  - verify: `TestLogicalAgentIdentityProjection_Scenario5_RotationPreservesLogicalIdentity`
- AC5.2: During the documented overlap both unexpired tokens verify; after the ADR-0300 retirement bound the old-key token fails and the new-key token remains valid.
  - verify: `TestADR_0301_LogicalAgentRotationOverlap`
- AC5.3: A deterministic compact-token/signature canary is proven present at the successful issuance boundary, then absent from the typed verifier result, bounded issue/verify errors and diagnostics, and every persistence-capable domain value. Separate rejected fixtures place recognizable tool, instance, and unknown-claim canaries in claim-derived error paths and prove errors/diagnostics do not echo them; successful signed claims and typed results retain the approved tool/instance fields. No session, event, status, or model-visible type is widened to carry a compact token or parent token.
  - verify: `TestADR_0301_LogicalAgentCredentialCanariesNeverPersistOrLeak`
