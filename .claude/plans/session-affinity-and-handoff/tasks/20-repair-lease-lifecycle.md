---
id: 20-repair-lease-lifecycle
title: Repair lease admission, child ownership, and drain races
blocked_by: [09-lease-loss-capability, 10-awaiting-lease-loss-handoff, 11-close-session-semantics, 12-graceful-drain-ownership]
status: in-progress
branch: ""
worktree: ".scratch/task-session-affinity-20"
issue: ""
retries: 0
last_error: "panel ship-blockers: provisional admission race, child lease invalidation gap, stale mutation admission, run lifecycle settlement, sequential drain, capability tombstones"
accumulator: acc/session-affinity-and-handoff
---

# Repair brief

Resolve the cross-confirmed lease lifecycle and drain findings without adding backend fencing. Preserve the documented boundary that an already-started backend call may complete.

- Register a cancellable provisional run admission before lease renewal loss can race engine/environment construction; lease loss prevents any provider/tool run from starting afterward.
- Route every Service-owned post-acquisition mutation through the exact held-lease capability and revalidate immediately before beginning each backend mutation.
- Make delegation-child/sessionLiveness leases grant and invalidate the same mutation capability before cancellation; after declared loss, cleanup must not release stale ownership.
- Do not remove a run lifecycle record on lease loss. Mark it control-ineligible/invalid, cancel it, and leave identity-safe removal plus `settled` closure to `FinishRun`.
- Make graceful drain observe settlements concurrently or recheck all pending runs at timeout; release settled ownership and retain only genuinely unjoined leases.
- Bound/remove normal-release mutation-capability entries so session churn cannot grow tombstones forever.
- Strengthen behavior tests across save/delete/event/tool/metadata paths and add a composition-level mecak8s lease/capability wiring proof.

Protects AC5.1–AC5.9, AC6.1–AC6.4, and AC7.1–AC7.7. Run race-focused tests plus full lint/test/docs/API gates. Commit locally; no push.
