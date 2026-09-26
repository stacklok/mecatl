---
id: 57-first-run-dead-end-repair
title: Remove first-run and recovery UX dead ends
blocked_by: []
status: done
branch: "c2b7a451"
worktree: ".scratch/task-microvm-57"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# UX repair

Fix review findings: pre-install failures say correct cause + rerun init, not unusable recover; recovery docs/commands require explicit session resume and never claim plain `--microvm` reopens; `--microvm --resume` rejects host-local sessions; init confirmation displays exact resolved paths/resources/egress/trust/download; validate delete target before confirmation; reject `--microvm` combined with explicit `--environment-profile`; public quickstart obtains the self-contained verified release binary/manifest with copy-paste commands.

Verification: each dead-end/error path, resume environment validation, path summary, flag conflict, delete prompt ordering, docs commands/full gates.
