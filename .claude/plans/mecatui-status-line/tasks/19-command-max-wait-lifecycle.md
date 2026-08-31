---
id: 19-command-max-wait-lifecycle
title: Add command debounce maximum-wait semantics
blocked_by: [18-direct-executable-command]
status: done
branch: plan-mecatui-status-line/19-command-max-wait-lifecycle
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Implement command trailing debounce plus refresh_interval maximum-wait behavior: templates immediate, commands coalesce input, no overlap, interval does not cancel active work, forced refresh deferred once, publish no semantic no-ops, preserve stale/default. Add deterministic lifecycle tests.
