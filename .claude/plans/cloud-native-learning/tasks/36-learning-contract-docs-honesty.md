---
id: 36-learning-contract-docs-honesty
title: Reconcile learning contract and documentation honesty
blocked_by: [31-documentation-generated-integration]
status: done
branch: plan-cloud-native-learning/36-learning-contract-docs-honesty
worktree: .scratch/worktrees/36-learning-contract-docs-honesty
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

> AC3.6: With learning unwired (including the default/off configuration without `--learning-store-url`), engine composition and ordinary runs remain byte-identical and allocate no attempt repository, worker, or durable attempt. Off mode with an explicitly configured remote learning store still dials, probes, and composes that repository set for explicit reflection, learned-skill inspection, and recovery of already-admitted work; an ordinary off-mode run does not itself admit a new automatic attempt, and explicit `/reflect` semantics remain unchanged.
>
> - verify: `TestCloudNativeLearning_Scenario3_UnwiredLearningIsByteIdentical`

> AC5.4: The shipped raw learning repository RPCs do not claim workload-authenticated ownership enforcement. With `OwnershipEnforced=true`, configuring `--learning-store-url` fails closed until ADR-0213 middleware, a private owner registry, and separated maintenance RPCs exist, regardless of a driver's self-advertised `enforced` value. Only a capability-complete driver explicitly trusted as single-tenant infrastructure may compose when `OwnershipEnforced=false`; proposal and skill project namespaces are opaque rather than raw workspace paths.
>
> - verify: `TestADR_0295_LearningDriversEnforceOwnershipOrFailClosed`

Add contract-level documentation and offline composition proofs for the multi-tenant fail-closed and
ownership-disabled trusted-single-tenant cases.
