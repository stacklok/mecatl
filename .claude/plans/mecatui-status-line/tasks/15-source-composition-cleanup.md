---
id: 15-source-composition-cleanup
title: Clarify status source composition terminology
blocked_by: [13-complete-status-input]
status: done
branch: plan-mecatui-status-line/15-source-composition-cleanup
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Perform only the reviewed terminology and composition cleanup. Rename status-facing `Transport` fields/functions to `ConnectionMode` (values `embedded`/`connect`) without changing filesystem provenance semantics. Split run-level config validation, status customization reading/validation, and status-source construction so `buildStatusSource` constructs only from validated customization. Rename `cloneSpans` to reflect its normalization behavior or split normalization from strict copying; pin repeat-normalization idempotence. Update affected docs/tests. Do not change source defaults, template schema, command execution, or lifecycle behavior.

## Acceptance criteria

Static and behavior tests pin the new names, composition separation, and idempotent normalization.
