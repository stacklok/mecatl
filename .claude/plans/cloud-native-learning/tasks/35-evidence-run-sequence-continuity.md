---
id: 35-evidence-run-sequence-continuity
title: Reject gaps in durable RunID evidence sequences
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

Repair finding: evidence continuity must reject actual missing or gapped durable `RunID` sequence
records. Do not rely only on reversed ordering checks or an optional fake-oracle capability. Make
the authoritative evidence path validate contiguous sequence evidence before reconstruction and
fail closed with the existing safe evidence-unavailable behavior.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the existing exact-source-evidence task.

> AC3.3: Missing, gap-marked, compacted-without-recoverable-archive, unauthorized, or digest/run-mismatched evidence terminally fails closed with a safe code and creates no proposal/skill mutation. No caller-supplied principal or system-principal bypass is accepted; inaccessible foreign or missing source evidence has the same absence-style result.
>
> - verify: `TestADR_0259_WorkerSourceAuthorityFailsClosedWithoutIdentityOracle`

Add offline proofs for a real missing sequence record and a gap in otherwise correctly ordered
records; both must reject without downstream mutation.
