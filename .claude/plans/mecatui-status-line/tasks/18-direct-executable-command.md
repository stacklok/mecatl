---
id: 18-direct-executable-command
title: Restrict status commands to explicit executables
blocked_by: [17-ui-status-submission-geometry]
status: done
branch: plan-mecatui-status-line/18-direct-executable-command
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Remove shell/source mode completely. Keep only an absolute executable plus literal args, reject every ambiguous/shell field, retain raw JSON stdin, exact env, CWD, bounds, and StatusML behavior. Update tests/docs/acceptance examples.
