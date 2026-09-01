---
id: 05-atomic-successor-preparation
title: Atomic successor preparation
blocked_by: [04-session-scoped-discovery-preparation]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Prepare internal clear and fork successor services. They must authorize and serialize against the source, obey lease and run-entry rules, reattach or bind the exact inherited or selected environment, and persist no partial successor on failure. Clear creates an empty-history successor; fork retains valid history and atomically authorizes any changed selector/provider/model/effort. Do not change protobufs; the public ClearSession RPC and final wire routing are deferred to the coordinated cutover.

Expected focus: internal server successor operations and tests only. Preserve existing public paths until task 09.

## Acceptance criteria

- AC4.2: Clear authorizes the source, observes source-state/run-entry serialization and
lease rules, reattaches/reauthorizes the inherited exact placement, and is
non-destructive: failure persists no successor and changes neither source nor client
binding.
  - verify: `TestADR_0280_ClearSessionIsLeaseSafeAndNonDestructive`
- AC4.3: `ForkSession` copies valid history and either inherits the exact source ref or
uses one of the three selector variants. A changed placement/provider/model/effort is
fully authorized and atomically bound; failure leaves no partial successor.
  - verify: `TestInvariant_fork_placement_is_atomic_and_server_authorized`
