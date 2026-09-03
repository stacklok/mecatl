---
id: 09-lease-loss-capability
title: Lease-loss mutation invalidation and optional compatibility
blocked_by: [08-session-mutation-lease-inventory]
status: done
branch: "plan-session-affinity-and-handoff/09-lease-loss-capability"
worktree: ".scratch/task-session-affinity-09"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Harden a held session lease into an explicit local mutation capability whose invalidation is atomic with declared renewal loss. Cancel the owning run, prevent every later application mutation from starting, and preserve the exact no-lease/unsupported fallback paths.

**Likely scope:** `internal/adapter/server/service.go` held-lease state, renew/loss/release paths, run/event/tool recorder wrappers, lease tests, and composition tests. Use `memlease`/programmable deterministic lease fakes; do not add backend fencing or token-bearing store APIs.

**Invariants:** invalidation happens before new saves/deletes/appends/tool records/metadata mutations; an operation already admitted may complete; lease identity is always the durable session ID across runs/retries/resume/compaction/controls; headers and run IDs are never ownership tokens. Nil `SessionLease` is byte-identical; `ErrLeaseUnsupported` remains one sticky diagnostic then no-lease fallback. Strict TDD and race-enabled offline proof.

## Acceptance criteria

- AC5.5: Lease-renewal loss cancels the owning run and locally invalidates its mutation
  capability before new application mutations begin. Later local saves, deletes,
  event appends, tool-call records, and metadata/sidecar mutations are prevented; an
  already-started storage call is explicitly allowed to complete until backend fencing
  exists.
  - verify: `TestSessionAffinityAndHandoff_Scenario5_LeaseLossCancelsAndPreventsNewMutations`

- AC5.8: With no `SessionLease` configured, admission, mutation, close, drain, and
  awaiting-approval behavior remain byte-identical to the current non-leased path: no
  lease acquisition or lease-loss invalidation is introduced. `ErrLeaseUnsupported`
  retains its existing sticky-disable diagnostic and fallback semantics.
  - verify: `TestADR_0290_OptionalLeaseCompatibilityAndUnsupportedFallback`

- AC5.9: Lease identity remains scoped to the durable session across runs, retries,
  awaiting resume, compaction, and controls; no run ID, header value, or gateway route
  becomes a lease or fencing token.
  - verify: `TestADR_0290_LeaseRemainsSessionScoped`
