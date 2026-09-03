---
id: 15-modeled-transport-bytes
title: Official-client transport-to-provider byte proof
blocked_by: [03-http-affinity-validation, 04-mecatui-affinity, 06-typescript-high-level-affinity, 14-modeled-rehydrate-repair]
status: done
branch: "plan-session-affinity-and-handoff/15-modeled-transport-bytes"
worktree: ".scratch/task-session-affinity-15"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Add one modeled transport fixture that starts from an official client, crosses the real gRPC or HTTP affinity validation boundary, reaches mecak8s Service run entry, and captures the provider request. Exercise exact legal bytes, header omission compatibility, and duplicate/illegal/mismatch rejection at ingress.

**Likely scope:** cross-layer offline tests under `internal/app`/`cmd/mecak8s`, reusable mecatui or TypeScript subprocess-free fixtures where practical, bufconn/httptest, and the fake provider capture from Task 14. Do not stand up a live gateway or external daemon.

**Invariants:** legal bytes are unchanged end-to-end; malformed/ambiguous variants perform no provider call; omission remains compatible; auth/ownership checks remain independent; test wording must call this modeled transport proof, not live Gateway/EndpointSlice verification. Strict TDD and offline-only gates.

## Acceptance criteria

- AC7.6: A modeled transport fixture receives one legal header from an official client,
  forwards it unchanged to mecak8s, and captures the identical bytes at the fake
  provider; omitting the field still completes through compatibility, while duplicate,
  illegal, and mismatched variants stop at mecak8s and never reach the provider.
  - verify: `TestSessionAffinityAndHandoff_Scenario7_ClientTransportProviderBytes`
