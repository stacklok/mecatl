---
id: 04-live-model-paths
title: Enforce caller ownership for live and model-facing operations
blocked_by: [01-ownership-core, 02-persisted-resources]
status: done
branch: "plan-caller-separation/04-live-model-paths"
worktree: ""
issue: "368"
retries: 0
last_error: ""
accumulator: acc/caller-separation
---

# Enforce caller ownership for live and model-facing operations

Apply the same decision to all live-run verbs and model-facing handles. Foreign calls
must be absence-equivalent and must not cause locks, signals, writes, append events, or
target-correlated diagnostics.

## Acceptance criteria

- AC3.1: Bob cannot prompt Alice's session twice; each attempt is refused as absent and neither creates a live run nor changes Alice's history.
  - verify: `TestCallerSeparation_Scenario3_RepeatedForeignPromptIsNotFound`
- AC3.2: Bob cannot approve, deny, cancel, persist, change mode, or approve a plan on Alice's live run; Alice can perform the corresponding operations on her own run. A foreign verb performs no run lookup with side effects, cancellation/approval send, run-entry lock acquisition, durable append, or target-correlated event/diagnostic; its response and unchanged target state match a missing handle.
  - verify: `TestCallerSeparation_Scenario3_LiveRunVerbsAreOwnerChecked`
- AC3.3: A model given another caller's subagent or team handle cannot inspect its transcript or act on it through an agent-facing tool.
  - verify: `TestCallerSeparation_Scenario3_ModelFacingHandlesAreOwnerChecked`
- AC3.5: The enforcement decision is evaluated for every request rather than cached at session creation or lease acquisition; repeated foreign live-run verbs before and after the owner's run completes make no mutation and remain indistinguishable from absent handles.
  - verify: `TestCallerSeparation_Scenario3_ForeignLiveRunReplayIsNotFound`
