---
id: 03-mecated-composition
title: Wire mecated listener topology and authority configuration
blocked_by: [01-service-authority]
status: done
branch: "plan-listener-scoped-workspace-authority/03-mecated-composition"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/listener-scoped-workspace-authority
---

# Task brief

Thread workspace authority through app composition and make mecated select it from parsed API-listener topology, with an explicit override. Validate a network filesystem deployment has an authoritative root before listener startup. Keep listener interpretation out of the server adapter and retain local client-selected roots/worktree behavior.

## Acceptance criteria

- AC1.5: A filesystem-bearing network configuration with no authoritative workspace fails configuration validation before either API listener starts. A no-FS deployment remains valid without a filesystem root.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario1_NetworkFilesystemRequiresConfiguredRoot`
- AC2.1: Under client-selectable workspace authority, an absolute workspace supplied by the local client creates a session rooted at that workspace even when it differs from the deployment default.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario2_LocalClientWorkspacePreserved`
- AC2.2: A loopback-only `mecated` configuration selects client-selectable authority. Any non-loopback, wildcard, or mixed public/loopback gRPC or HTTP/SSE listener selects server-assigned authority using the existing fail-closed parsed-address classifier, including IPv6 loopback coverage.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario2_MecatedPolicyFollowsAPIListenerTopology`
- AC2.3: A reverse-proxied loopback deployment can explicitly select server-assigned authority; an explicit policy selection overrides the topology-derived local default.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario2_ExplicitAuthorityOverridesTopology`
- AC2.4: Existing worktree discovery and session binding still accept a client-selected local workspace, while `ListWorktrees` remains inert for a server-assigned request whose workspace is empty.
  - verify: `TestADR_0032_WorktreeBindingRemainsClientSelectableOnly`
