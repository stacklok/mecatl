---
id: 07-delegation-and-artifact-boundary
title: Delegation placement affinity and typed artifact handles
blocked_by: [04-exact-reattachment]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Remove placement selection and physical fork roots from model-facing and direct delegation surfaces. Teams derive placement from their owner session, while any direct API selector goes through authorized `Bind`; subagents and Parallel continue to receive/fork the parent's complete runtime environment, and no-FS can never upgrade. Introduce or tighten distinct typed preserved-fork, delegation, and artifact handles so placement IDs and artifact handles are not interchangeable. Inspection must reauthorize durable ownership and original placement scope before resolving a private artifact. Replace fork-root/path projections in delegation events, tool results, inspection surfaces, and model-visible summaries with opaque typed handles and bounded non-path metadata while preserving the existing redaction and lifecycle families.

Expected focus: `engine/agent` Subagent/Parallel/Team result and inspection paths, `engine/session/event.go` delegation payloads, server direct-Team/inspection handlers, mapper/client projections, and focused tests. Use task 03's wire shapes; avoid central create/reattach, discovery, schedules, ACP, and docs.

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
