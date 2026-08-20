---
id: 15-authority-resource-identity
title: Bind Cedar file resources to physical workspace identity
blocked_by: [11-vertical-repair]
status: done
branch: plan-authority-evaluator-port/15-authority-resource-identity
worktree: ""
issue: "371"
retries: 0
last_error: ""
accumulator: acc/authority-evaluator-port
---

# Repair brief

Panel blocker: derive Cedar file resource descriptors using the same physical/confined identity as filesystem access, not lexical Clean alone. A symlink under an allowed path must not evade a denied target subtree. Add a regression proof.
