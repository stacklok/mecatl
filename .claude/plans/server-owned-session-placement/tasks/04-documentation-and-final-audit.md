---
id: 04-documentation-and-final-audit
title: Documentation and final audit
blocked_by: [03-atomic-server-owned-placement-cutover]
status: done
branch: "plan-server-owned-session-placement/04-documentation-and-final-audit"
worktree: ".scratch/worktrees/04-documentation-and-final-audit"
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Perform the documentation-only final audit after the atomic cutover. Update the living
architecture and usage documentation, implementation notes, ADR 0027 List 1/List 2, and
user documentation; regenerate the configuration reference and validate the site. Inventory the random
selector HMAC key as Build-owned process-lifetime state and record that restart resets it
by design, invalidating selectors until clients relist. Do not change implementation;
report any implementation discrepancy for follow-up rather than repairing it in this task.

Expected focus: `docs/architecture.md`, `docs/usage.md`,
`docs/design/IMPLEMENTATION-NOTES.md`, `docs/adr/0027-cloud-native.md`, `AGENTS.md`,
`user-docs/`, generated configuration reference, and `task site:build`.

## Acceptance criteria

- AC8.3: ADR 0027 List 1 inventories the random HMAC key as a Build-owned,
process-lifetime resource, and List 2 records its reset-by-design restart semantics.
Selectors are not persisted, restart requires relisting, and no selector registry/map is
introduced.
  - verify: none — the ADR 0027 resource inventory is reviewed by humans; `task docs` checks its citations and structure
