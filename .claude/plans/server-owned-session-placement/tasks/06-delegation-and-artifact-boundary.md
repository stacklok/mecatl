---
id: 06-delegation-and-artifact-boundary
title: Delegation and artifact boundary
blocked_by: [05-atomic-successor-preparation]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Move delegation to the unified environment identity: child constructors derive identity from `tool.Environment.Ref()`, while Teams derive from their owning session and Subagent/Parallel derive or fork the parent environment. Introduce distinct typed artifact, preserved-fork, and delegation handles with no physical paths, and require inspection to reauthorize owner and original placement scope. Prepare the server boundary for direct Team handling, but defer its wire routing to task 09.

Expected focus: engine delegation identity and artifact types, inspection services, redacted result/event projections, and tests. No model-facing or delegation call may accept an unrelated placement ID or path.

## Acceptance criteria

- AC5.1: Teams derive placement from their owning session (or a direct API's authorized
selector); subagents and Parallel derive or fork the parent environment. No model-facing
or delegation call accepts an unrelated placement ID or path, and no-FS cannot upgrade.
  - verify: `TestInvariant_delegation_cannot_escalate_placement`
- AC5.2: Preserved-fork, delegation, and artifact handles are distinct typed handles,
never accepted as placement selectors. Every inspection consumer reauthorizes the owner
and original placement scope before access.
  - verify: `TestADR_0280_ArtifactHandlesCannotReplayAsPlacementSelectors`
- AC5.3: Delegation results, inspection surfaces, and model-visible summaries expose no
fork root or placement path.
  - verify: `TestADR_0280_DelegationObservabilityContainsNoPlacementPath`
