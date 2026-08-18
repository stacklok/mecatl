---
id: 01-v2-snapshots
title: Versioned current snapshot core
blocked_by: []
status: done
branch: "plan-session-storage-continuity/01-v2-snapshots"
worktree: ""
issue: "586"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Implement the adapter-private v2 current-snapshot core: one bounded current snapshot, v1 read compatibility and lazy per-session promotion with complete sessnap/logical-mtime fidelity, and unchanged append-only tool/event sidecars. Keep `port.SessionStore` unchanged and format-neutral. Define verified-v2 authority over coexisting v1, but leave crash injection/disk failure mechanics and cross-process orphan-temp locking to the dependent tasks.

Follow ADR-0226, ADR-0217, ADR-0027, ADR-0104, AGENTS.md layering/security invariants, and existing repository conventions. Keep tests offline. Do not absorb catalog, migration-job, cleanup, health, or TUI work. Update living docs/user docs touched by the core format.

## Acceptance criteria

- AC1.1: Saving a same-sized session 1,000 times leaves steady-state snapshot storage bounded to one current snapshot plus bounded metadata and one in-progress temporary replacement.
  - verify: `TestSessionStorageContinuity_Scenario1_BoundedRepeatedSaves`
- AC1.3: V1 snapshots remain loadable and a successful lazy promotion preserves conversation, usage, counters, state, profile, selector, environment reference, owner, kind, relationship, title/provenance, logical modification time, and sidecars; `unknown` remains `unknown`.
  - verify: `TestSessionStorageContinuity_Scenario1_V1PromotionFidelity`
