---
id: 14-modeled-rehydrate-repair
title: Modeled Redis rehydration, repair, and correlation
blocked_by: [01-session-header-contract, 13-modeled-crash-ownership]
status: done
branch: "plan-session-affinity-and-handoff/14-modeled-rehydrate-repair"
worktree: ".scratch/task-session-affinity-14"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Extend the modeled takeover fixture through authoritative Redis reload, sidecar visibility, `StateRunning` crash repair via `Session.Abandon`, persisted tool-pair closure, and continuation on the same durable session. Capture the successor's first provider request to prove end-to-end durable session correlation while allowing a new run ID.

**Likely scope:** the Scenario 7 integration fixture/tests, `internal/adapter/server` run-entry repair only if the existing seam needs lease-capability integration, Redis sidecar assertions, and a local fake provider HTTP capture.

**Invariants:** acquire ownership before repair; mutate the loaded aggregate through `Abandon`; persist repair before continuing; unanswered tool calls become valid pairs; provider header comes from the successor run context, not ingress forwarding; no storage-level fencing or exactly-once external-effect claim. Entirely offline, deterministic, strict TDD.

## Acceptance criteria

- AC7.4: The new owner rehydrates the latest Redis snapshot and sidecar state, detects
  the crash-orphaned `running` state, repairs unanswered tool-call pairing through
  `Session.Abandon`, persists the repair, and then continues the same durable session.
  - verify: `TestSessionAffinityAndHandoff_Scenario7_RehydrateRepairAndContinue`

- AC7.5: The first provider request after continuation carries the exact same durable
  session ID as client ingress, while its run ID may correctly be new. Storage-level
  fencing of a delayed old owner's already-started call is not claimed.
  - verify: `TestADR_0291_HandoffEndToEndCorrelation`
