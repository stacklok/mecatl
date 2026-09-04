---
id: 45-remote-production-e2e-final-docs
title: Prove remote production learning and reconcile final documentation
blocked_by: [43-bounded-attempt-callback-lifecycle, 44-reservation-before-create-fencing]
status: done
branch: plan-cloud-native-learning/45-remote-production-e2e-final-docs
worktree: .scratch/worktrees/45-remote-production-e2e-final-docs
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Repair brief

Repair finding: add a production-shaped remote E2E using a real `Build`, a complete gRPC learning
driver, and `LearningStoreURL`. Exercise explicit admission through the service/relay, replace the
Build, discover and complete the remote worker attempt, and verify the public attempt, proposal, and
skill surfaces. The proof must be deterministic (one attempt) and demonstrate no local fallback.
Reconcile documentation only if this final behavior changes it.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the preceding behavioral tasks.


> AC3.5: Crash after claim or after a downstream durable boundary is reconciled idempotently; a retry
> neither duplicates a proposal/skill nor reports an invented success.

> AC6.2: Concurrent replicas enforce one configured global automatic count/token budget, cooldown,
> and deduplication window without multiplying spend or durable attempts.
