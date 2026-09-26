---
id: 25-delegation-operations-repair
title: Wire production delegation, observability, doctor, and placement persistence
blocked_by: [21-root-composition-lifecycle-repair]
status: done
branch: "plan-microvm-execution-environments/25-delegation-operations-repair"
worktree: ".scratch/task-microvm-25"
issue: "534"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Panel repair brief

Spec blockers: no concrete ForkDriver/daemon fork-merge operations are wired; OperationsObserver/Doctor are library-only; production quotas are disabled. Security medium: FileSessionPersister uses only a process mutex and can lose placements across daemons.

Implement daemon/client fork, merge, and child cleanup operations and register the microVM EnvironmentForker/Merger through root composition. Wire bounded observer events and an operator-invokable doctor into microvmd without creating a duplicate unexported telemetry dead end; either integrate the existing observer or replace it with the smallest exported operational seam. Wire quotas through production lifecycle. Add inter-process locking and directory durability to placement persistence.

Protects AC6.3, AC7.1–AC7.5, AC8.3–AC8.4.

## Verification

- Real composition creates isolated child VM/worktree, conflict-aware merges, and durable cleanup.
- Production daemon emits bounded metrics/diagnostics and doctor is invokable.
- Two independent persisters cannot lose concurrent entries.
- Existing delegation/operations/admission tests plus lint/test/docs pass.
