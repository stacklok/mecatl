---
id: 35-end-to-end-client-crash-quota-proof
title: Prove mecatui through daemon and crash-orphan quota convergence
blocked_by: [33-artifact-immutability-release-evidence, 34-doctor-egress-metrics-correctness]
status: done
branch: "plan-microvm-execution-environments/35-end-to-end-client-crash-quota-proof"
worktree: ".scratch/task-microvm-35"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Third panel repair

Final Spec blocker: live E2E tests app.Build and mecatui separately, manufactures fresh evidence rather than release-compatible payloads, and deletes a VM before restart instead of proving crash-orphaned ready-state quota convergence.

Drive the actual mecatui/session adapter over gRPC into app.Build and microvmd with selected profile and resolved paths. Consume release-compatible strict artifact evidence. Crash/restart microvmd while a ready generation still owns quota, then prove secure reattach or identity-checked destruction and quota convergence. Retain ordered streaming/cancel/delegation/egress/doctor controls.

Protects AC1.1, AC2.1, AC5.6, AC6.3, AC8.1.

Verification: local Linux KVM complete client-to-daemon journey passes; crash-orphan quota converges without pre-delete; CI matrix runs equivalent live cells; lint/test/docs/site/action/ac-trace pass.
