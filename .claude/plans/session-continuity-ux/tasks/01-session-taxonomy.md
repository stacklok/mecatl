---
id: 01-session-taxonomy
title: Durable session kind and relationship metadata
blocked_by: []
status: done
branch: "plan-session-continuity-ux/01-session-taxonomy"
worktree: ".scratch/worktrees/session-continuity-ux-task-01"
issue: "471"
retries: 0
last_error: ""
accumulator: acc/session-continuity-ux
---

# Task brief

Add the ADR-0108 closed SessionKind/relationship value objects and trusted-producer stamping without changing existing required API signatures. Round-trip the metadata through sessnap, store metadata, event-source creation metadata, every in-tree store/driver, and their conformance suites. Public create/fork/carryover remains main-only. Promote ADR-0108 to Accepted only after the implementation is assembled by the orchestrator; do not edit the plan/ADR shared files in this worker.

## Acceptance criteria

- AC1.1: Main, scheduled, Subagent, Parallel-branch, and team-member sessions round-trip the exact kind and relationship fields required by ADR-0108 through snapshot, metadata-list, event-source creation metadata, and every in-tree store/driver adapter.
  - verify: `TestSessionContinuityUX_Scenario1_KindRelationshipRoundTrip`
- AC1.2: Public create/fork/carryover requests can create only `main`; callers cannot stamp or override scheduled/child relationships.
  - verify: `TestADR_0108_PublicCreateCannotForgeKind`
- AC1.3: Invalid kind/relationship combinations fail closed at construction or restore rather than granting replay/continuation capabilities.
  - verify: `TestADR_0108_InvalidRelationshipsFailClosed`
- AC1.4: Peer forks, effort forks, and model carryovers remain `main` and do not acquire lineage fields, preserving ADR-0065.
  - verify: `TestSessionContinuityUX_Scenario1_PeerRebindsRemainMain`
- AC1.5: The work retains the existing `session.New` signature and required `SessionStore` interface; every intentional engine API change is additive and classified `Added`, with no `Changed` or `Removed` baseline entry.
  - verify: `TestInvariant_session_continuity_api_is_additive`
