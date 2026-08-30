---
id: 14-three-tier-template-schema
title: Implement per-surface full compact minimal templates
blocked_by: [12-source-owned-defaults, 13-complete-status-input]
status: done
branch: plan-mecatui-status-line/14-three-tier-template-schema
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Replace two-document wide/compact configuration with strict independent `header` and `footer` full/compact/minimal variants. Source selects each surface independently using submitted available widths. Update validation, defaults, generator templates, tests, acceptance examples, ADR/docs/user docs. Command configuration remains explicitly unsupported until task 05. Preserve automatic template escaping and semantic StatusML.

## Acceptance criteria

Protect AC2.2, AC2.3, AC2.6, and template documentation examples.
