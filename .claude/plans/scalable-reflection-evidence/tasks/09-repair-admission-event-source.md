---
id: 09-repair-admission-event-source
title: Repair bounded automatic admission and complete event selection
blocked_by: []
status: in-progress
attempt: 1
branch: plan-scalable-reflection-evidence/09-repair-admission-event-source-attempt-1
worktree: .scratch/worker-scalable-reflection-evidence-09-repair-admission-event-source-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/scalable-reflection-evidence
---

# Repair brief

Repair panel blockers: automatic admission must not construct an unbounded `learning.Input`; make its borrowed-source admission contract context-aware and cancellable so `Built.Close` and request cancellation interrupt it. Explicit reflection must stream/rank the complete event source and apply `MaxInputEvents` only after selected-event admission. Preserve deterministic manifest/provenance and all resource bounds. Also charge final bounded request bytes including existing facts to coordinator queue accounting, and ensure manifest event coordinates are session-wide unique. Add focused offline regressions. Run lint/test/docs/api update as needed; commit only on the task branch; do not push.
