---
id: 52-public-docs-adr-polish
title: Make public microVM onboarding concise and fix lifecycle references
blocked_by: [49-first-run-empty-xdg-e2e]
status: done
branch: "plan-microvm-execution-environments/52-public-docs-adr-polish"
worktree: ".scratch/task-microvm-52"
issue: "526"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Documentation round

Update public user docs to lead with a minimal first-run→daily-use→recovery journey, safe defaults, what remains on host, where edits live, and links to the operator runbook. Fix stale ADR links/numbers and ensure navigation reaches the page. Reconcile ADR 0224 metadata/status and point-in-time version references with the landed decision (without rewriting accepted rationale beyond status/link/version corrections allowed before acceptance). Ensure README/architecture/usage/config reference/acceptance/IMPLEMENTATION-NOTES and user docs use consistent command names, alias, paths, OCI terminology, platform matrix, and schedule/remote limitations.

Run task docs, site build, citation/link checks; report any implementation-doc mismatch rather than documenting nonexistent behavior.
