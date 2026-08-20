---
id: 12-authority-restore-validation
title: Fail closed on incomplete persisted authority
blocked_by: [11-vertical-repair]
status: done
branch: "plan-authority-evaluator-port/12-authority-restore-validation"
worktree: ""
issue: "371"
retries: 0
last_error: "task test baseline macOS /var symlink failures"
accumulator: acc/authority-evaluator-port
---

# Repair brief

Panel blocker: restore of a claimed authority must reject omitted/null/empty capability-set payloads before binding. Preserve legacy unbound records. Add regression tests for `{}` and null capability_set through sessnap restore.
