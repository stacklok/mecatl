---
id: 07-repair-docs-and-acceptance
title: Reconcile acceptance and architecture after panel repairs
blocked_by: [06-redis-ledger-hardening]
status: done
branch: "acc/persistent-read-ledgers"
worktree: ""
issue: "888"
retries: 0
last_error: ""
accumulator: acc/persistent-read-ledgers
---

# Repair-wave brief

Reconcile docs with the approved repair design. Revert the in-place edit to frozen ADR 0208. Update ADR 0290, the acceptance plan, AGENTS.md, architecture/ports, implementation notes, and ADR-0027 lifecycle tables to state: Workspace owns content operations only; Environment separately carries a mandatory session/child ledger; all children get fresh ledgers while base-sharing/direct-write children retain the exact content backend and namespace; durable deletion is atomic with Redis session deletion; FileVersion has narrow opaque serialization. Correct AC3.1/AC3.6 and tool-facing claims: a failed RecordRead establishes no new evidence, but existing evidence remains valid only when normal version equality and final CAS still pass; do not claim unconditional invalidation. Return the plan to landed only after all named tests resolve. Regenerate llms.txt with task docs and run strict traceability.
