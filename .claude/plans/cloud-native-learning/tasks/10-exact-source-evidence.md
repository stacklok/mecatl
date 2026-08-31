---
id: 10-exact-source-evidence
title: Reconstruct exact-source canonical learning evidence
blocked_by: [09-durable-attempt-admission]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Build the composition-owned evidence loader for an attempt's private owner/session/RunID/digest binding. Reconstruct only through the existing bounded canonical learning projection using persisted session events and compaction archives. Validate ownership, exact run, ordering, gap state, archive completeness, and digest before returning data. Foreign and missing sources must be absence-equivalent; raw transcript/event/tool/archive values never leave this boundary.

## Acceptance criteria

- AC3.3: Missing, gap-marked, compacted-without-recoverable-archive, unauthorized, or digest/run-mismatched evidence terminally fails closed with a safe code and creates no proposal/skill mutation. No caller-supplied principal or system-principal bypass is accepted; inaccessible foreign or missing source evidence has the same absence-style result.
  - verify: `TestADR_0254_WorkerSourceAuthorityFailsClosedWithoutIdentityOracle`
