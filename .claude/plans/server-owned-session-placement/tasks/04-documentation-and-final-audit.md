---
id: 04-documentation-and-final-audit
title: Documentation and final audit
blocked_by: [03-atomic-server-owned-placement-cutover]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Perform the documentation-only final audit after the atomic cutover. Update the living architecture and usage documentation, implementation notes, ADR 0027 List 1/List 2, and user documentation; regenerate `llms.txt` and validate the site. Do not change implementation; report any implementation discrepancy for follow-up rather than repairing it in this task.

Expected focus: `docs/architecture.md`, `docs/usage.md`, `docs/design/IMPLEMENTATION-NOTES.md`, `docs/adr/0027-cloud-native.md`, `AGENTS.md`, `user-docs/`, generated `llms.txt`, and `task site:build`.

## Acceptance criteria

- AC8.3: The ADR 0027 List 1/List 2 re-audit records any actual added resource or durable
state. V1 introduces none of a registry, signer, cache, or process-local placement map.
  - verify: `TestADR_0280_PlacementReauditFindsNoV1RegistryOrState`
