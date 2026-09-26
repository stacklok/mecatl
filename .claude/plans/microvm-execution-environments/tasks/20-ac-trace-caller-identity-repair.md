---
id: 20-ac-trace-caller-identity-repair
title: Repair pre-existing caller-identity traceability record
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/20-ac-trace-caller-identity-repair"
worktree: ".scratch/task-microvm-20"
issue: ""
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Repair brief

The assembled microVM plan has 43 ACs and zero trace failures, but the repository-wide `task ac-trace-strict` gate is blocked by the pre-existing landed `docs/acceptance/caller-identity.md`, which has zero structured `ACx.y` criteria. Convert that completed record to the current structured acceptance format without changing its landed behavior or inventing new implementation requirements. Map each existing observable assertion to an existing grep-resolvable proof test; preserve issue/ADR/status/history and citations. Run the bundled acceptance-plan check, `task ac-trace-strict`, and `task docs`.

This is a terminal integration repair, not part of the microVM feature's numbered AC ownership.
