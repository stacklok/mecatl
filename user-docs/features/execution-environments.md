---
sidebar_position: 330
title: Execution environments
description:
  Choose the workspace for a session and resume its files and shell commands
  safely.
---

# Execution environments

An execution environment provides a session's file operations and shell commands.
When you create a session, use the deployment default, choose **None** for a
file-less session, or select an eligible execution template.

## Choose an environment

The session-creation API accepts an `execution` selection:

- Omit `execution` to use the deployment default.
- Set `execution: {"none": {}}` to create a file-less session.
- Set `execution: {"template": {"id": "<TEMPLATE_ID>", "revision":
  "<TEMPLATE_REVISION>"}}` to use one exact template revision advertised by the
  deployment.

Templates are operator-owned immutable execution definitions. Their catalog
shows only templates that the authenticated caller may use. Listing a template
does not reserve capacity. At session creation, the server checks the selected
ID and revision again. If the template is no longer available or has no capacity,
create a new session after choosing another option.

Use `/execution` in `mecatui` or **Execution** in the Studio chat composer. Both
show **Deployment default**, **None**, and eligible templates. See [Work in the
TUI](/mecatui/using-the-tui.md#start-a-session-with-an-execution-choice) or
[Mecatl Studio web UI](/building/deployment/studio.md#choose-execution-for-a-new-chat).

## Default workspace

A default session with a workspace root provides `Read`, `ListDir`, `Write`,
`Edit`, `Copy`, `Move`, `Remove`, `Grep`, `Glob`, and `Shell`. File tools preserve
read-before-edit and compare-and-swap checks: read a file before replacing it,
and read it again if another process changes it. `Remove` is non-recursive, and
`Copy` and `Move` refuse an existing destination.

The default can be a local workspace, an embedded workspace, or an operator
configured remote execution environment. Client requests do not supply paths,
container images, credentials, or private environment identities.

## File-less sessions

A file-less session has no file tools, built-in `Shell`, Parallel, or SkillDraft.
It retains the file-less catalog, including supported web, memory, MCP, skill,
Subagent, and Team tools. Children use the same file-less tool set.

The selection is fixed for the session lifetime. Create another session to use a
different environment.

## Child environments

Delegation mode determines where work runs:

- Read-only Subagents and team members receive a child environment. In a trusted
  workspace, that can be a worktree.
- Parallel branches use independent copies.
- Mutating members and direct-write Subagents use the parent environment. Their
  mutations run serially with sibling tool calls.

Agent-facing shells receive a secret-scrubbed environment. Provider keys,
`MECATL_*` credentials, cloud credentials, and other secret-shaped variables are
removed before a command runs.

## Resume a session

Mecatl persists an environment identity with the session. On resume, it
reattaches that exact identity through the deployment's placement provider. It
never substitutes the current default. If the provider cannot reattach the
recorded environment, the run fails instead of using a different workspace.

For Kubernetes execution, the environment is backed by a retained PVC. The
provider retains the PVC when a session closes, an executor is replaced, or the
provider chart is uninstalled. A later session resume reattaches the recorded
environment and workspace when it remains available.

## Kubernetes execution lifecycle

The optional Kubernetes execution provider creates a namespaced
`ExecutionEnvironment` for an exact template revision. Provider replicas use
Kubernetes compare-and-swap operations and run claims to coordinate access. A
provider restart does not clear another operation's claim.

If an operation expires or loses ownership, the environment is fenced. Restore
provider readiness and use the administrator lifecycle operation with the exact
environment, epoch, Pod UID, and PVC UID. Do not remove provider finalizers or
delete the custom resource to bypass this recovery path.

Retiring an environment stops its executor and retains its PVC. Deleting retained
storage is a separate administrator operation that requires an idle retired
environment and the recorded PVC UID. See [Deploy Kubernetes execution](/building/deployment/mecak8s.md#kubernetes-execution-provider)
for configuration, recovery, and retention procedures.

## Secure the provider connection

The execution provider accepts its server certificate, client trust bundle, and
client authorization manifest from platform-managed Kubernetes Secrets. The chart
does not issue certificates or manage a CA. The manifest grants each mTLS client
an exact URI identity, allowed template IDs, and optional administrator scope.
Platform PKI proves the connecting client identity; it does not grant template,
owner, run, file, or command authority by itself.

Keep provider and `mecak8s` client files at their configured projected paths. The
provider and client reload valid TLS material independently. Renewing a leaf
certificate does not change the policy generation. Increment the manifest
`generation` only when the client authorization policy changes. During a CA
rotation, publish both roots on both ends, rotate leaves, then remove the old
root after connections use the new chain.

## Next steps

- [Deploy Kubernetes execution](/building/deployment/mecak8s.md#kubernetes-execution-provider)
  for templates, mTLS, and lifecycle operations.
- [Work in the TUI](/mecatui/using-the-tui.md) to choose an environment for a new
  session.
- [Permissions and posture](/features/permissions-and-posture.md) to control tool
  approvals.
