---
id: 33-canonical-artifact-identity
title: Persist canonical artifact identity across worker restarts
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

Repair finding: make initial and restarted workers use one persisted canonical digest and partition
identity for deterministic downstream artifact IDs. Where a remote repository transforms an opaque
partition, perform that transformation before deterministic ID derivation, or checkpoint and reuse
the repository's authoritative returned ID. Reconciliation must never recompute a conflicting
identity from local assumptions.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the existing downstream claim-fencing task.

> AC4.2: Crash and claim-loss races converge through deterministic IDs and CAS: no duplicate artifact, overwrite of a newer target revision, two active versions, invented attempt success, or partition crossing. An independently valid late downstream commit is allowed; authoritative reread adopts only a compatible deterministic artifact, otherwise leaves inactive/unlinked residue or reaches safe non-success.
>
> - verify: `TestADR_0259_IndependentDownstreamCommitReconcilesAfterClaimLoss`

Add an offline restart proof covering opaque remote partition transformation and authoritative-ID
checkpointing without duplicate or cross-partition artifacts.
