---
id: 36-learning-contract-docs-honesty
title: Reconcile learning contract and documentation honesty
blocked_by: [31-documentation-generated-integration]
status: in-progress
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Repair brief

Repair finding: update the learning contract and documentation to state that multi-tenant remote
learning fails closed until ADR-0213 enforcement exists, and that a trusted single-tenant driver is
permitted only when ownership is disabled. Reconcile the ADR index's Accepted status, off-mode
remote repository/resource documentation, and semantic inventory references. Preserve byte-identical
off/unwired composition and do not claim enforcement that the driver cannot provide.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the existing composition and driver-ownership tasks.

> AC3.6: With learning unwired or off, engine composition and ordinary runs remain byte-identical and allocate no attempt repository, worker, or durable attempt.
>
> - verify: `TestCloudNativeLearning_Scenario3_UnwiredLearningIsByteIdentical`

> AC5.4: Driver-backed learning enforces workload-authenticated claims; caller and infrastructure RPCs are separated; project namespaces are opaque rather than raw workspace paths; and startup fails closed when the driver cannot enforce ownership.
>
> - verify: `TestADR_0259_LearningDriversEnforceOwnershipOrFailClosed`

Add contract-level documentation and offline composition proofs for the multi-tenant fail-closed and
ownership-disabled trusted-single-tenant cases.
