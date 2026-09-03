---
id: 23-repair-transport-oracles-final
title: Final repair of affinity SDK semantics and behavioral tests
blocked_by: [21-repair-affinity-clients-tests]
status: done
branch: "plan-session-affinity-and-handoff/23-repair-transport-oracles-final"
worktree: ".scratch/task-session-affinity-23"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Final repair brief

Resolve every surviving transport/client/test blocker:

- `withSessionAffinity` must reject an illegal explicit ID with an actionable synchronous SDK error; never delete a caller header and return an apparently bound call. High-level APIs retain documented compatibility for unrepresentable external IDs only where they do not explicitly request binding.
- Make each independently released provider module test-self-contained: module-local embedded/vector fixtures or equivalent. Preserve one canonical repository source through generation/parity if useful, but published module tests must not read above module root.
- Strengthen HTTP and gRPC route matrices: exact-header behavior must equal the captured headerless baseline outcome for each route/RPC, while mismatch/duplicate always reaches common pre-dispatch InvalidArgument. Avoid “not 400/InvalidArgument” weak assertions.
- Replace metadata-shape-only Converse control tests with live fixtures proving approval, cancel, child-cancel, steer, and steer-cancel target only the established first session; assert second-session prompt rejection and second session unchanged.
- Keep retry-wrapper and bounded provider concurrency tests; ensure source/debug CreateSession and official clients remain covered.
- Keep ac-trace names meaningful without brittle source-substring mirrors. Update ADR/plan/docs/API reports if the invalid-ID contract changes.

Protects AC1.*, AC2.*, AC3.*, AC4.*, AC8.*. Run SDK, race, API, lint/test/docs/site/ac-trace gates. No protobuf/Gateway resource changes.
