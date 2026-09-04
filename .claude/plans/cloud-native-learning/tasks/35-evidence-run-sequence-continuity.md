---
id: 35-evidence-run-sequence-continuity
title: Validate monotonic durable RunID evidence sequences
blocked_by: [31-documentation-generated-integration]
status: done
branch: "plan-cloud-native-learning/35-evidence-run-sequence-continuity"
worktree: ".scratch/worktrees/35-evidence-run-sequence-continuity"
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Repair brief

Repair finding: evidence continuity must reject a missing initial event, duplicate/decreasing event
sequences, and explicit durable gap records for a durable `RunID`. EventLog append order is
canonical, but numeric `Seq` values need only start at one and increase strictly: the recorder
coalesces deltas under their first sequence number, so a later record may legitimately have a gap.
Do not rely only on reversed ordering checks or an optional fake-oracle capability. Make the
authoritative evidence path preserve that distinction before reconstruction and fail closed with the
existing safe evidence-unavailable behavior.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the existing exact-source-evidence task.

> AC3.3: Missing, gap-marked, compacted-without-recoverable-archive, unauthorized, or digest/run-mismatched evidence terminally fails closed with a safe code and creates no proposal/skill mutation. No caller-supplied principal or system-principal bypass is accepted; inaccessible foreign or missing source evidence has the same absence-style result.
>
> - verify: `TestADR_0295_WorkerSourceAuthorityFailsClosedWithoutIdentityOracle`

Add offline proofs for a production-shaped coalesced sequence with a numeric gap, duplicate and
decreasing sequences, a missing initial sequence one, and an explicit durable gap record; only the
coalesced sequence must reconstruct evidence and reach downstream reflection.
