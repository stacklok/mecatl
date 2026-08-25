---
id: 02-internal-authority-paths
title: Apply workspace authority to schedules and adoption
blocked_by: [01-service-authority]
status: done
branch: "plan-listener-scoped-workspace-authority/02-internal-authority-paths"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/listener-scoped-workspace-authority
---

# Task brief

Make scheduled fires and legacy adoption consume the shared Service workspace-authority policy rather than independently accepting durable workspace values. Server-assigned filesystem schedules must be stamped with the deployment root, no-FS schedules stay workspace-free, and legacy off-root durable state must fail before claim or environment construction. Preserve local/embedded adoption behavior.

## Acceptance criteria

- AC5.3: A scheduled fire under server-assigned authority has this profile matrix: no-FS schedules carry no workspace; a filesystem schedule is stamped with the configured root by the server, not a stored or tool-supplied client path; an explicit filesystem schedule in the no-FS mecak8s deployment is rejected. A legacy off-root schedule fails before claim, session creation, environment creation, or filesystem access and records an operator-visible error.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario5_ScheduledFireCannotReviveOffRoot`
- AC5.4: `CreateSession`, persisted-session run entry/rehydration, scheduled fires, adoption preflight/adoption, and composition-created environment overrides each apply server-assigned authority or use the configured deployment environment. No path attaches a client- or snapshot-supplied off-root filesystem session.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario5_AllCreationPathsRespectAuthority`
