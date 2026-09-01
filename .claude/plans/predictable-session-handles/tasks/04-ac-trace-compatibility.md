---
id: 04-ac-trace-compatibility
title: Restore session handle traceability aliases
blocked_by: [01-handle-projection-presentation, 02-debug-handle-resolution, 03-command-help-and-docs]
status: in-progress
branch: ""
worktree: ".scratch/worktrees/issue-922-task04"
issue: "922"
retries: 0
last_error: ""
accumulator: acc/predictable-session-handles
---

# Task brief

Repair the aggregate ac-trace regression introduced when task 03 renamed tests consumed by the already-landed session-continuity plan and reused the AC3.3 proof body for AC3.4. Preserve the old verification names as thin compatibility proofs, restore the new AC3.3 named proof, and keep the ADR-0278 evidence-boundary proof without duplicating the test logic. Do not revert the new handle behavior or modify unrelated ac-trace baseline failures.

## Acceptance criteria

- AC3.3: Header, `/sessions`, debugger-target presentation, terminal title, status input, and
  shipped StatusML templates use the same handle grammar. The cross-boundary table below proves
  its edge cases and that only ordinary presentation changes; `InspectSession` scope/history
  handles, evidence digests, and target+incarnation cryptographic handles remain unchanged.
  - verify: `TestPredictableSessionHandles_Scenario3_PresentationParityAndSafety`
- AC3.4: Preserve the verification names consumed by already-landed acceptance plans as thin
  compatibility proofs while README summaries and new tests use ADR-0217/0278 handle terminology;
  no ordinary projection is described as a debugger evidence digest.
  - verify: `TestADR_0278_OrdinaryHandleDoesNotAlterDebuggerEvidenceHandles`
