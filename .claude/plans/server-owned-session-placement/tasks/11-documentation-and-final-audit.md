---
id: 11-documentation-and-final-audit
title: Documentation and final audit
blocked_by: [10-remove-durable-workspace]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Perform the final documentation-only audit of the completed placement protocol. Update living architecture, usage, implementation notes, operator and user documentation, and the ADR 0027 List 1/List 2 re-audit. Regenerate `llms.txt` and validate documentation links. Do not change implementation; report any implementation discrepancy for follow-up rather than repairing it in this task.

Expected focus: `docs/architecture.md`, `docs/usage.md`, `docs/design/IMPLEMENTATION-NOTES.md`, `docs/adr/0027-cloud-native.md`, `AGENTS.md`, `user-docs/`, and generated `llms.txt`.

## Acceptance criteria

- AC8.3: The ADR 0027 List 1/List 2 re-audit records any actual added resource or durable
state. V1 introduces none of a registry, signer, cache, or process-local placement map.
  - verify: `TestADR_0280_PlacementReauditFindsNoV1RegistryOrState`
