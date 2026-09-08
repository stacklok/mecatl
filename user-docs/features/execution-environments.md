---
sidebar_position: 7
title: Execution environments
description: Understand workspaces, shells, forks, and persisted execution environments in Mecatl.
---

# Execution environments

An execution environment binds a session to the namespace in which its tools
operate. It contains:

- an in-process `EnvironmentRef` identity;
- a non-nil workspace for file operations; and
- an optional command runner bound to that same namespace.

The runner is not given a per-call working directory. This prevents a tool from
accidentally reading files from one namespace while executing commands in
another.

## Availability

The default profile is available in `mecated`, `mecak8s`, mecatui's embedded
server, and engine embeddings. A session can instead select the `no-fs` profile
for research, coordination, or remote deployments that must not expose a local
filesystem.

## Default workspace

Create a default session with a workspace root. `Read`, `ListDir`, `Write`,
`Edit`, `Copy`, `Move`, `Remove`, `Grep`, `Glob`, and `Bash` all use that root.
The workspace enforces the file-operation safety protocol: existing files must
be read before overwrite, edits use exact and unique matches, and writes use
version-aware conditional replacement so a concurrent change is never silently
clobbered. Namespace operations are narrower: removal is non-recursive, copy
accepts only regular files, and copy and move refuse an existing destination.

The version ledger belongs to the live workspace/environment instance. A new
run or process may require a fresh `Read` before an `Edit` or existing-file
`Write`; this is intentional fail-safe behavior, not lost application data.

## The no-filesystem profile

Create a no-filesystem session by setting `profile: "no-fs"`:

```console
curl -s -X POST http://127.0.0.1:8081/v1/sessions \
  -d '{"profile":"no-fs"}'
```

The profile requires an empty `workspace`. Any other profile value is rejected;
there is no silent fallback. A no-FS catalog removes `Read`, `ListDir`,
`Write`, `Edit`, `Copy`, `Move`, `Remove`, `Grep`, `Glob`, `Bash`, `BashStatus`,
`Parallel`, and `SkillDraft`. It retains
web tools, memory, MCP tools, skills, `Subagent`, and `Team`; children use the
same file-less surface and cannot create a shell or fork a workspace.

The profile is fixed at session creation. The model cannot switch it during a
run. See [Core tools](/building/what-you-get/core-tools.md) for the complete catalog.

## Child environments

Different delegation modes use different environment strategies:

- **Read-only Subagent and team members** get a child environment. When the
  workspace is trusted, the harness can use a worktree so child changes are
  isolated. An untrusted workspace withholds the read-only child shell because
  creating a worktree requires operating on the repository's `.git`.
- **Parallel branches** use hardened force-copy environments. The initial copy
  does not invoke Git; each branch then works in its own copied namespace.
- **Mutating members and direct-write Subagents** use the parent environment and
  intentionally modify the real workspace. They are serialized against sibling
  tool calls; there is no merge-back step.
- A child environment's runner is built for the same child root. A failed
  worktree reservation can fall back to a copy, but the runner remains bound to
  the environment that was actually returned.

All agent-facing shells use a secret-scrubbed environment. Provider keys,
`MECATL_*` credentials, cloud credentials, and other secret-shaped variables are
removed before a command runs.

## Persistence and reattachment

The environment identity is persisted with a session snapshot. In-tree local
and no-FS environments are reconstructed by composition. A non-local identity
can be reattached only when the deployment supplies an
`EnvironmentResolver`; a missing resolver, mismatched identity, or nil workspace
fails loudly rather than falling back to a local workspace.

Environment reattachment and engine rehydration are separate operations. A
persisted provider/model selection or no-FS profile can require rebuilding the
per-session engine, while the environment resolver supplies the live workspace
and runner.

## Limitations

- No-FS sessions cannot use local file tools, shell commands, workspace forks, or
  parallel branches.
- Read-only child shells depend on project trust; this is separate from the
  parent's ability to run its own shell.
- Direct-write children can leave partial edits if cancelled or interrupted;
  the parent workspace and Git are the rollback boundary.
- Environment identity is an in-process binding for local deployments. Remote
  reattachment requires an explicit resolver and is not supplied by default.

## Next steps

- [Background Bash](/building/what-you-get/core-tools.md#background-commands)
- [Subagents, teams, and parallel](/building/what-you-get/subagents-teams-parallel.md)
- [Workspace trust](https://github.com/stacklok/mecatl/blob/main/docs/usage/workspace-trust.md)
- [Capability and deployment matrix](./capability-matrix.md)
