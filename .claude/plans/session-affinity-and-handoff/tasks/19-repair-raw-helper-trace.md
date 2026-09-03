---
id: 19-repair-raw-helper-trace
title: Repair TypeScript raw-helper acceptance trace
blocked_by: [05-typescript-raw-affinity]
status: in-progress
branch: ""
worktree: ".scratch/task-session-affinity-19"
issue: ""
retries: 0
last_error: "ac-trace strict could not resolve TestADR_0290_TypeScriptRawHelperCompatibility"
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Add the missing grep-locatable Go acceptance proof for the already-implemented TypeScript raw session-affinity helper. The proof must validate the meaningful public compatibility contract and fail if the helper/header composition is removed; do not duplicate the TypeScript implementation or weaken the acceptance plan.

## Acceptance criteria

- AC4.4: Additive TypeScript raw helpers can bind an explicit session ID on either
  transport without changing generated protobuf code; calls without the helper retain
  their current behavior.
  - verify: `TestADR_0290_TypeScriptRawHelperCompatibility`
