---
id: 16-modeled-awaiting-takeover
title: Modeled awaiting-approval takeover
blocked_by: [10-awaiting-lease-loss-handoff, 13-modeled-crash-ownership]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Extend the two-Service fake-clock fixture with an owner parked on a durable permission ask, definitive renewal loss, local retraction/invalidation, TTL progression, single-successor takeover, and exact `PendingAsk` resumption.

**Likely scope:** Scenario 7 integration tests plus shared lease/awaiting test helpers; production changes only at the Service/relay seam if the cross-Service proof exposes a gap.

**Invariants:** the stale process cannot resolve or execute the ask; the durable awaiting snapshot is unchanged across loss; no cancellation/replacement is persisted; exactly one successor resumes the same ask and executes at most once after ownership. Use `mockllm`, `memfs`, miniredis/fake lease time, deterministic synchronization, and strict TDD.

## Acceptance criteria

- AC7.7: A modeled owner that loses renewal while awaiting approval retracts only its
  local ask delivery and cannot resolve the ask. After TTL/takeover, exactly one
  successor reloads the unchanged durable awaiting snapshot and resumes that exact
  `PendingAsk`, rather than a cancellation or replacement.
  - verify: `TestSessionAffinityAndHandoff_Scenario7_AwaitingLeaseLossHandoff`
