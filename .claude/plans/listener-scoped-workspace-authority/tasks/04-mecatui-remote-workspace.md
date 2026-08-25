---
id: 04-mecatui-remote-workspace
title: Keep remote mecatui workspace-free
blocked_by: []
status: done
branch: "plan-listener-scoped-workspace-authority/04-mecatui-remote-workspace"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/listener-scoped-workspace-authority
---

# Task brief

Change mecatui connect handling so a remote target never resolves or transmits the client cwd by default, and rejects an explicit workspace locally before session creation. Preserve embedded and loopback cwd/worktree workflows. This is client privacy and UX hardening; it does not replace server enforcement.

## Acceptance criteria

- AC3.1: A non-loopback `mecatui connect` invocation with no explicit workspace sends an empty workspace in `CreateSession`; it does not resolve or transmit the client cwd.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario3_RemoteConnectSendsEmptyWorkspace`
- AC3.2: Embedded and loopback connect paths still resolve an omitted workspace to the local cwd and send that absolute path.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario3_LocalConnectPreservesWorkspaceDefault`
- AC3.3: A non-loopback `mecatui connect` invocation with an explicit workspace is rejected locally before it resolves a cwd or sends `CreateSession`. A stale or non-mecatui client remains subject to the server's `InvalidArgument` rejection; neither path may create a client-selected workspace.
  - verify: `TestListenerScopedWorkspaceAuthority_Scenario3_RemoteExplicitWorkspaceIsRejectedLocally`
