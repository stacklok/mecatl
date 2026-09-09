---
id: 07-workspace-rpc
title: Guest Workspace RPC and version-aware conformance
blocked_by: [04-control-protocol, 06-virtiofs-assets]
status: done
branch: "plan-microvm-execution-environments/07-workspace-rpc"
worktree: ".scratch/task-microvm-07"
issue: "530"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Implement the guest/host Workspace methods and adapter over the authenticated protocol. Preserve all current FileVersion, read-ledger, create-only, conditional-replace, path confinement, Glob/Grep, and bounded-result semantics. Extend the shared filesystem/environment conformance suites rather than inventing parallel rules.

## Acceptance criteria

- AC3.4: Read/Edit/Write/Grep/Glob and Bash observe the same bytes and cwd. A Workspace
  read followed by Bash mutation is visible to a later Workspace read, and vice versa.
  - verify: `TestInvariant_environment_workspace_runner_affinity`
- AC3.5: Workspace ReadVersion/CreateFile/ReplaceFile preserve opaque versions,
  create-only behavior, and backend-atomic conditional replacement; a concurrent host
  or guest change yields a model-visible conflict instead of overwriting newer bytes.
  - verify: `TestMicroVMEnvironments_Scenario3_RemoteWorkspaceConformance`
