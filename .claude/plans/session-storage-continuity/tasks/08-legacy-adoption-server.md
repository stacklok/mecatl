---
id: 08-legacy-adoption-server
title: Server-side legacy adoption
blocked_by: [02-indexed-inventory, 02b-bounded-pagination, 02c-inventory-locking]
status: done
branch: "repair/08-adoption-integration"
worktree: ""
issue: "593"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Add capability-driven server preflight and caller-bound idempotent adoption that copies one eligible owned unknown transcript into a new explicit-main session, keeps the source unchanged, and never escalates reserved provenance.

Follow ADR-0226, ADR-0217, ADR-0027, ADR-0104, AGENTS.md layering/security invariants, and existing repository conventions. Keep tests offline. Do not absorb later tasks or weaken fail-closed behavior. Update the living docs and user-docs affected by this task.

## Acceptance criteria

- AC7.1: Preflight returns a capability and stable reason code; only an `unknown` source owned by the authenticated caller identity from trusted context—not request-supplied owner metadata—that is complete, valid, terminal/idle, tool-paired, and free of child/team/parallel/scheduled provenance is eligible.
  - verify: `TestSessionStorageContinuity_Scenario7_AdoptionEligibilityMatrix`, `TestSessionStorageContinuity_Scenario7_OwnershipComesFromCallerContext`
- AC7.2: Workspace/environment and provider/model bindings are displayed and explicit; unresolved bindings block adoption rather than silently using the current default, and cross-provider provider-private replay state is stripped through the existing carryover rule.
  - verify: `TestSessionStorageContinuity_Scenario7_ExplicitBindingsAndProviderNeutrality`
- AC7.3: Adoption revalidates every precondition while holding the mutation/run-entry lease and uses an idempotency key bound to the authenticated caller and source so retry/lost-response returns the same complete target rather than creating duplicates; cross-caller replay is existence-indistinguishable. Success atomically publishes one new opaque main ID with equivalent authoritative transcript and source audit relationship, while cancellation/failure leaves the source unchanged and no partial target visible.
  - verify: `TestSessionStorageContinuity_Scenario7_NewMainCopyPreservesSource`, `TestSessionStorageContinuity_Scenario7_AdoptionIdempotency`, `TestSessionStorageContinuity_Scenario7_CrossCallerReplayDenied`
- AC7.4: Adoption never occurs automatically or in bulk, reserved provenance remains permanently inspect-only, and ownership failures do not become an ID-existence oracle.
  - verify: `TestSessionStorageContinuity_Scenario7_NoEscalationOrOracle`
