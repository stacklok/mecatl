---
id: 22-repair-lease-races-final
title: Final repair of lease admission, drain, and mutation boundaries
blocked_by: [20-repair-lease-lifecycle]
status: in-progress
branch: ""
worktree: ".scratch/task-session-affinity-22"
issue: ""
retries: 0
last_error: "second panel: provisional run launch, drain approval/awaiting persist races, child lost registration, steer admission, inventory boundary"
accumulator: acc/session-affinity-and-handoff
---

# Final repair brief

Resolve every surviving lease/concurrency blocker:

- Replace check-then-launch with a real provisional lifecycle registered before renewal loss or drain can race acquisition/engine construction. Lease loss/drain cancels provisional admission; engine Run/Retry/Resume uses that cancellation and cannot start after loss. `mutationLeaseContext` must fail closed when leasing is configured but the exact hold is absent.
- Drain refuses live approval/resume admission after its gate is armed.
- Make awaiting persistence and drain atomic at the lifecycle level: drain cannot observe a false non-awaiting state while the durable awaiting save is admitted; failed save must resolve the marker honestly.
- Child `sessionLiveness.Register` rejects an existing lost hold and requires fresh acquisition; loss invalidates before callback snapshot/cancel, and stale cleanup never releases ownership.
- Keep run lifecycle records through `FinishRun`; settle channels on every removal. Atomically validate lease/control eligibility for steer and cancel before enqueue.
- Clean capability/held-lease tombstones once stale references have settled, while retaining denial during unwind.
- Funnel session-family durable mutations through narrow capability-checked Service wrappers; forbid direct Store/EventLog/ToolCallRecorder mutation outside sanctioned wrappers so the structural inventory cannot be bypassed by free helpers/new selector names.
- Add meaningful post-loss event append and app/mecak8s composition wiring tests.

Protects AC5.*, AC6.*, AC7.*. Strict race TDD; all gates. No backend epoch fencing.
