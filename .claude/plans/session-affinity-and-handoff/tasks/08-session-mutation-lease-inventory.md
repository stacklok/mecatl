---
id: 08-session-mutation-lease-inventory
title: Lease-owned session mutation inventory
blocked_by: [01-session-header-contract]
status: done
branch: "plan-session-affinity-and-handoff/08-session-mutation-lease-inventory"
worktree: ".scratch/task-session-affinity-08"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Create an explicit, source-level inventory of every application mutation to a durable session family and route each classified mutator through one Service-owned session-scoped lease capability. Cover snapshot saves/deletes, event append, tool-call recording, derivative metadata and sidecars, `SetMode`, compaction, rename, retention/cleanup/migration/adoption, retry preparation, stale repair, and relay persistence without moving lease knowledge into `engine/agent`.

**Likely scope:** `internal/adapter/server/service.go`, event recorder and management files, `classification.go` or a sibling mutation-classification table/test, composition recorder wiring, and focused store spies. Avoid widening `SessionStore`, `EventLog`, or `ToolCallRecorder` with fencing tokens.

**Invariants:** ownership/auth checks precede caller-selected coordination; `runEntryMu` precedes lease acquisition; the lease is session-scoped and Service/composition-owned; read-only methods and composition-time setters are explicitly classified; adding a new mutator must fail the guard until classified. An already-started backend call is outside the guarantee. Strict TDD, deterministic fakes, offline tests.

## Acceptance criteria

- AC5.1: `SetMode` and every out-of-band session-family mutation acquires or proves the
  same session-scoped lease before changing a snapshot, deleting a family, appending an
  event/tool sidecar, or changing derivative metadata.
  - verify: `TestSessionAffinityAndHandoff_Scenario5_MutationLeaseInventory`

- AC5.2: The inventory test fails when a new session-bound mutator is added without an
  explicit lease-ownership classification; read-only operations and composition-time
  setters are explicitly distinguished.
  - verify: `TestADR_0290_AllSessionMutatorsClassified`
