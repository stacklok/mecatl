---
id: 06-clear-and-fork-successors
title: Atomic non-destructive clear and fork successors
blocked_by: [04-exact-reattachment]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Implement path-free `ClearSession` and redesign `ForkSession` around atomic successor creation. Both operations must owner-authorize the source, serialize through the source's run-entry mutex/liveness/state checks and optional lease, and reauthorize/reattach the inherited or explicitly selected placement before any successor save. Clear creates a distinct empty-history session inheriting the exact placement ref, owner, applicable mode, provider/model/effort, limits, and permission posture. Fork retains valid copied-history semantics, inherits the exact ref when placement is omitted, or invokes `Bind` for explicit no-FS/opaque ID while validating provider/model/effort and placement as one operation. Persist/register only after every check succeeds; failures leave source, client binding, and store unchanged.

Expected focus: a dedicated server successor file plus narrow handler methods/tests and client calls using task 03's generated RPC shapes. Avoid discovery, delegation, schedules, ACP, docs, and generated protobuf edits.

## Acceptance criteria

- AC4.1: `ClearSession` is a new breaking RPC: request contains only `source_session_id`;
response returns the distinct successor ID and safe session metadata. It creates an
empty-history successor with the source's exact `PlacementRef`, owner, applicable mode,
provider/model/effort, limits, and permission posture.
  - verify: `TestADR_0280_ClearSessionRPCCreatesEmptyInheritedSuccessor`
- AC4.2: Clear authorizes the source, observes source-state/run-entry serialization and
lease rules, reattaches/reauthorizes the inherited exact placement, and is
non-destructive: failure persists no successor and changes neither source nor client
binding.
  - verify: `TestADR_0280_ClearSessionIsLeaseSafeAndNonDestructive`
- AC4.3: `ForkSession` copies valid history and either inherits the exact source ref or
uses one of the three selector variants. A changed placement/provider/model/effort is
fully authorized and atomically bound; failure leaves no partial successor.
  - verify: `TestInvariant_fork_placement_is_atomic_and_server_authorized`
