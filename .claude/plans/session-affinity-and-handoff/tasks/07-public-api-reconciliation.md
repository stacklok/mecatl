---
id: 07-public-api-reconciliation
title: Assembled public API and protobuf reconciliation
blocked_by: [01-session-header-contract, 04-mecatui-affinity, 05-typescript-raw-affinity, 06-typescript-high-level-affinity]
status: done
branch: "plan-session-affinity-and-handoff/07-public-api-reconciliation"
worktree: ".scratch/task-session-affinity-07"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Perform the serial, assembled public-surface reconciliation after all engine, mecatui, and TypeScript affinity changes have landed. Compare intended additive symbols/helpers against engine and SDK API reports, refresh reports only through repository tasks where required, and prove no protobuf source or generated output changed.

**Likely scope:** `engine/api/*.txt`, `engine/CHANGELOG.md`, `sdk/typescript/etc/*.api.md`, plus inspection of `contracts/proto/`, `contracts/gen/`, and `sdk/typescript/src/gen/`. Do not introduce behavior here; repair an unexpected surface by reporting mis-decomposition rather than normalizing it into the reports.

**Invariants:** generated reports are single-writer reconciliation surfaces; only intended additive helpers/shared symbols may appear; frozen protobuf wire contracts remain byte-identical. Run `task api:check`, all SDK API gates, and the exact plan inspection command.

## Acceptance criteria

- AC4.5: Public mecatui/engine and TypeScript API reports change only by the intended
  additive helpers and shared symbols; generated protobuf output is unchanged.
  - verify: inspection — compare engine and TypeScript API reports and `git diff -- contracts/proto contracts/gen sdk/typescript/src/gen`
