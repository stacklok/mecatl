---
id: 04-docs-and-layering
title: Accept ADR 0290 and reconcile architecture and lifecycle docs
blocked_by: [02-fork-ledger-isolation, 03-redis-ledger]
status: done
branch: "plan-persistent-read-before-write-ledgers/04-docs-and-layering"
worktree: ""
issue: "888"
retries: 0
last_error: ""
accumulator: acc/persistent-read-ledgers
---

# Task brief

Reconcile the landed implementation with the living contract. Accept ADR 0290 and add the permitted supersession pointer to ADR 0208 decision 6 only. Update `AGENTS.md`, architecture/ports, implementation notes, and ADR-0027's resource/fidelity ledgers so they describe independently selected session-scoped ledgers, the fresh in-memory default, durable Redis lifecycle/cleanup, fail-closed errors, fork isolation, and unchanged final filesystem CAS. Update indexes if needed. Do not redesign behavior or modify implementation. Regenerate `llms.txt` through `task docs`; if private matlatl authentication is unavailable, report the external blocker rather than hand-editing generated output.

## Acceptance criteria

- AC1.5: The ledger and Workspace contracts remain in `engine/tool`; neither imports a root adapter or Redis dependency.
  - verify: `TestNoCoreImportsAdapter`, `TestCoreImportDirection`, `TestNoCyclesAmongCore`
