---
id: 14-authority-evaluator-presence
title: Fail closed for bound sessions without an evaluator
blocked_by: [11-vertical-repair]
status: done
branch: plan-authority-evaluator-port/14-authority-evaluator-presence
worktree: ""
issue: "371"
retries: 0
last_error: ""
accumulator: acc/authority-evaluator-port
---

# Repair brief

Panel blocker: a bound session must never bypass authority because Deps.AuthorityEvaluator is nil. Make absence valid only through the explicit noop/no-authority composition mode, or reject the combination. Add execution regression coverage.
