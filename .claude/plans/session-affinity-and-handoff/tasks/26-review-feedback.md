---
id: 26-review-feedback
title: Address human review blockers and contract drift
blocked_by: [25-k8s-force-delete-regression]
status: done
branch: "plan-session-affinity-and-handoff/26-review-feedback"
worktree: ".scratch/task-session-affinity-26"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Review feedback brief

Address the blocking review and directly related correctness/compatibility findings:

1. `GracefulDrain.settle` must honor sticky `preserveDurable`, not only mutable `awaiting`; drive a real relay test and prove durable `PendingAsk` remains.
2. Make the full pod termination budget fit and operator-configurable: separate drain/grpc/http/close bounds as appropriate and expose chart `terminationGracePeriodSeconds`; document/test the inequality including preStop delay.
3. Route in-stream `ResumeApproval` through a Service-owned live-run approval gate ordered with lease invalidation; child asks included.
4. Replace source-string Build wiring checks with a real `app.Build` behavioral lease/mutation proof.
5. Strengthen mutation inventory: require discovered writes to use the mutating class, include `schedule_manager.go`, reuse the real unsanctioned-write scanner in falsifiability tests.
6. Preserve future oneof compatibility: explicitly reject second Prompt/Retry, ignore unknown/unset frames as before.
7. Follow ADR 0294 for high-level TS get/fork/debug paths: unrepresentable external IDs omit affinity; only explicit `withSessionAffinity` throws, and degrade removes stale caller affinity headers.
8. Move the root transport contract out of `engine/port` to a root transport/package location that does not make engine own unused HTTP ingress API and does not violate mecatui client layering. Remove engine API/changelog additions if no longer required. Keep provider release independence and test parity.
9. Bound legal affinity header length and add reflective/structural guards where practical so new session RPC/routes cannot bypass validation.

Do not add Gateway resources or backend fencing. Preserve all existing ACs and the force-delete fix. Run full Go/SDK/docs/site/API/ac-trace/deploy gates and focused race tests.
