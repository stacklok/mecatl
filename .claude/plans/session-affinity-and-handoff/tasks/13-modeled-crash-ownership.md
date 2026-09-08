---
id: 13-modeled-crash-ownership
title: Modeled crash stream drop and TTL takeover
blocked_by: [09-lease-loss-capability, 12-graceful-drain-ownership]
status: done
branch: "plan-session-affinity-and-handoff/13-modeled-crash-ownership"
worktree: ".scratch/task-session-affinity-13"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Build the reusable two-Service, shared-Redis/miniredis, fake-clock ownership fixture and prove hard owner death rather than graceful release. The killed owner's client stream must drop without a fabricated terminal; survivors are blocked before TTL and exactly one wins after expiry.

**Likely scope:** an offline integration fixture under `internal/app` or `cmd/mecak8s`, k8s-lease-compatible deterministic fake/memlease control, miniredis store setup, and named scenario tests. Reuse real Service run-entry and lease APIs rather than simulating their results.

**Invariants:** no real network/model/Kubernetes dependency; pre-TTL attempts perform no mutation, rehydration, or provider call; owner death does not call `Release`; post-TTL concurrent acquisition has one winner. The fixture models lease semantics only and makes no EndpointSlice/Gateway claim. Strict TDD and race-enabled concurrency proof.

## Acceptance criteria

- AC7.1: The modeled killed owner drops its active client stream without fabricating a
  success or terminal event.
  - verify: `TestSessionAffinityAndHandoff_Scenario7_KilledOwnerDropsStream`

- AC7.2: Before lease TTL expiry, every survivor request for that session cannot acquire
  ownership, mutate durable state through newly admitted application work, or start a
  provider call.
  - verify: `TestADR_0291_PreTTLRequestsCannotAcquireOrRun`

- AC7.3: After TTL expiry, exactly one modeled survivor acquires the Kubernetes lease;
  concurrent survivors cannot both start ownership work or rehydration.
  - verify: `TestADR_0291_PostTTLSingleSurvivorAcquires`
