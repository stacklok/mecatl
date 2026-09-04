---
id: 07-documentation
title: Status-line operator, public, and architecture documentation
blocked_by: [02-status-settings, 05-command-mode, 06-refresh-lifecycle, 08-status-contract-corrections]
status: done
branch: plan-mecatui-status-line/07-documentation
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Update `docs/tui.md`, `docs/architecture.md`, relevant `user-docs/`, and ADR 0027 resource inventory if applicable. Document the user-global settings location/schema, shared input, StatusML surfaces/tokens/templates, shell/executable modes, CWD provenance, exact environment, process/output limits, refresh/failure behavior, embedded/connect parity, restart-only reload, and copyable template-clock plus inline shell examples. State the v1 mapping is best effort and link #799 for the stable cross-widget contract. Run generated docs; never hand-edit the configuration reference.

## Acceptance criteria

- AC5.1: `docs/tui.md` and `user-docs/` document the settings location/schema, shared input, StatusML, templates, command mode, and safety limits with copyable examples.
  - verify: inspection — user/operator documentation is updated with the shipped schema
- AC5.2: The documentation distinguishes the best-effort v1 StatusML token mapping from the stable cross-widget token contract owned by #799.
  - verify: inspection — #799 is linked from theme/status documentation
