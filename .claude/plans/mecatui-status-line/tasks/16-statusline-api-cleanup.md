---
id: 16-statusline-api-cleanup
title: Make statusline public API package-idiomatic
blocked_by: [15-source-composition-cleanup]
status: done
branch: plan-mecatui-status-line/16-statusline-api-cleanup
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Rename public statusline API to Source/Input/Result and constructors to New*Source; remove the revive exception. Add named sourceRenderer type. Finish ConnectionMode/composition terminology and rename clone normalization accurately with idempotence pin. Update code-facing docs/ADRs/tasks only. Do not change UI submission, command policy, lifecycle, or user docs.
