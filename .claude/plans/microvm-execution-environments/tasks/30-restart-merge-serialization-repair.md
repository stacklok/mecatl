---
id: 30-restart-merge-serialization-repair
title: Converge stale generations and serialize exact-base merges in daemon
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/30-restart-merge-serialization-repair"
worktree: ".scratch/task-microvm-30"
issue: "532"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Second panel repair

Cross-confirmed Spec/Security blockers: fresh LibkrunBackend still cannot rediscover active runtime; unavailable ready records and reconstructed quota stay forever. Production child fork records only committed HEAD, not exact dirty base; daemon merge check/apply lacks shared per-parent serialization.

Persist enough runner/start/endpoint/runtime identity to securely reopen an exact generation, or identity-check and destroy orphan on daemon restart. Startup reconciliation must converge unavailable ready records and release quotas within an explicit policy, never leave unmanaged workload. Capture exact parent worktree state at fork. Serialize the complete check/patch/apply transaction inside microvmd with a per-parent lock across all clients and revalidate immediately before atomic apply.

Protects AC5.2–AC5.6, AC6.3, AC7.4–AC7.5.

Verification: fresh daemon/backend reattaches or safely destroys real/fake generation; stale ready quotas converge; dirty-parent fork/merge exact; multi-client concurrent merge yields one apply and one conflict; lint/test/docs pass.
