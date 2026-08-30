---
id: 12-source-owned-defaults
title: Move default and absent-surface behavior into source
blocked_by: [11-source-naming-settings]
status: done
branch: plan-mecatui-status-line/12-source-owned-defaults
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Remove UI legacy status-line fallback/variant ownership. Eliminate `ui.Deps.StatusLine`, model legacy status fields, UI full/compact/minimal source variants, and `activeStatusLine`. Make the source own shipped default surfaces, independent header/footer selection, missing-surface defaults, and last-good/stale state. UI retains only latest `Result` plus mandatory chrome/theme/final layout. Pin negative architecture checks and default compatibility.

## Acceptance criteria

Protect AC2.1, AC2.3, AC2.4, AC2.6, and AC5.3.
