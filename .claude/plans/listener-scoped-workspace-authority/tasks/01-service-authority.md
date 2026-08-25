---
id: 01-service-authority
title: Service workspace-authority policy and persisted-root enforcement
blocked_by: []
status: done
branch: "plan-listener-scoped-workspace-authority/01-service-authority"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/listener-scoped-workspace-authority
---

# Task brief

Add the injected Service-level workspace-authority policy. It must preserve local client-selectable behavior, but in server-assigned mode reject any non-empty direct request before path processing or environment construction and assign the configured root to filesystem sessions. Apply the same policy to persisted-session run entry, rehydration, and environment overrides. Stored roots use the plan's lexical, fail-closed identity rule; do not put listener topology or filesystem authorization in the engine.

## Acceptance criteria

- AC1.1: With server-assigned filesystem authority and a configured deployment root, a default-profile `CreateSession` request whose workspace is empty creates a session rooted at that configured root.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario1_EmptyWorkspaceUsesConfiguredRoot`
- AC1.2: With server-assigned filesystem authority, every non-empty requested workspace—including whitespace, relative, traversal, and an exact textual match of the configured root—is rejected as `InvalidArgument` with a message explaining that the deployment assigns the workspace. Rejection occurs before path cleaning, filesystem access, trust evaluation, or environment creation; the value is neither compared with nor silently replaced by the configured root.
  - verify: `TestInvariant_server_assigned_workspace_requires_empty_request`
- AC1.3: An explicit no-FS request remains valid with an empty workspace under server-assigned authority and is rejected with a non-empty workspace under the existing no-FS contract.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario1_ServerAssignedProfileMatrix`
- AC1.4: The same acceptance and rejection behavior is observed through both the gRPC and HTTP session-create surfaces, because they share the Service policy.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario1_GrpcAndHTTPAgree`
- AC5.1: Resuming or rehydrating a persisted filesystem session whose stored root is not the configured root under the stored-root identity contract fails with a clear failed precondition before any workspace factory, environment resolver, or filesystem access receives that stored path.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario5_PersistedOffRootSessionFailsClosed`
- AC5.2: The persisted-root identity contract rejects a relative path, traversal spelling, symlink alias, or a root made stale by a configuration change. It accepts only the same cleaned absolute configured root without resolving symlinks.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario5_PersistedRootIdentityIsLexicalAndFailClosed`
