---
id: 13-authority-principal-identity
title: Preserve exact owner identity in evaluator requests
blocked_by: [11-vertical-repair]
status: done
branch: "plan-authority-evaluator-port/13-authority-principal-identity"
worktree: ""
issue: "371"
retries: 0
last_error: "task test baseline macOS /var symlink failures"
accumulator: acc/authority-evaluator-port
---

# Repair brief

Panel blockers: do not fabricate an owner for ownerless sessions; fail closed when evaluator identity requires one. Replace colon-joined issuer/subject owner encoding with an injective/provider-neutral representation and ensure Cedar receives distinct exact identity data. Add collision and ownerless regression tests.
