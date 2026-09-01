---
id: 18-downstream-claim-fencing
title: Fence attempt-owned lifecycle transitions by claim
blocked_by: [17-distributed-repository-conformance]
status: in-progress
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: "Corrected mis-decomposition: downstream repositories converge independently of attempt claims."
accumulator: acc/cloud-native-learning
---

# Task brief

Fence only AttemptRepository's claim-owned `Renew`, `Checkpoint`, `Release`, and `Finalize`
transitions. Enforce legal CAS so an expired, released, superseded, retried, or abandoned claim
cannot revive, checkpoint, release, or finalize its attempt; cannot finalize a successor; and
cannot rewrite an abandoned attempt to success. Do not add claim guards to proposal, skill, or
catalog commits: they are independent deterministic/CAS-protected durable boundaries. Failure
injection must show that a late independently valid downstream commit never proves attempt
success and is reconciled by compatible adoption, inactive/unlinked residue, or safe non-success.

Kill criterion: strict universal prevention of late downstream writes requires a separately designed
unified linearizable learning authority and is out of scope for this task.

## Acceptance criteria

- AC2.3: Attempts permit only legal CAS transitions. An expired, released, superseded, retried, or abandoned claim cannot renew, checkpoint, release, or finalize its attempt; cannot finalize a successor; and cannot rewrite an abandoned attempt to success.
  - verify: `TestADR_0254_StaleClaimCannotTransitionAttempt`
- AC4.2: Crash and claim-loss races converge through deterministic IDs and CAS: no duplicate artifact, overwrite of a newer target revision, two active versions, invented attempt success, or partition crossing. An independently valid late downstream commit is allowed; authoritative reread adopts only a compatible deterministic artifact, otherwise leaves inactive/unlinked residue or reaches safe non-success.
  - verify: `TestADR_0254_IndependentDownstreamCommitReconcilesAfterClaimLoss`
