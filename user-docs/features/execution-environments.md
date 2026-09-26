---
sidebar_position: 330
title: Execution environments
description:
  Keep file operations, shell commands, forks, and resumed sessions in the
  correct workspace.
---

# Execution environments

An execution environment keeps a session's file operations and shell commands in
one workspace. Choose the default profile for workspace tasks or `no-fs` for
sessions that need no local file access.

## Availability

The default profile is available in `mecated`, `mecak8s`, `mecatui`'s embedded
server, and engine embeddings. A local deployment can set `microvm-local` as that
default so filesystem and shell tools run in a repository-scoped VM. Linux amd64
with KVM is the qualified path. An unmerged Darwin arm64 path exists for Apple
Silicon macOS 15+ with Hypervisor.framework, but native and signed-release
qualification remain pending, so it is experimental rather than released support.
See [Local microVM environments](/building/deployment/microvm-environments.md) for
platform requirements and the source qualification procedure.

A session can instead select the `no-fs` profile
for research, coordination, or remote deployments that must not expose a local
filesystem.

## Default workspace

Create a default session with a workspace root. `Read`, `ListDir`, `Write`,
`Edit`, `Copy`, `Move`, `Remove`, `Grep`, `Glob`, and `Shell` all use that root.
The workspace requires a read before overwriting an existing file. Edits use
exact, unique matches, and writes fail if the file changed after the read.
Removal is non-recursive, copy accepts only regular files, and copy and move
refuse an existing destination.

A new run or process may require another `Read` before `Edit` or an
existing-file `Write` because the version record belongs to the live
environment. This fail-safe check does not mean file data was lost.

## The no-filesystem profile

Create a no-filesystem session by setting `profile: "no-fs"`:

```sh
curl -s -X POST http://127.0.0.1:8081/v1/sessions \
  -d '{"profile":"no-fs"}'
```

The profile requires an empty `workspace`. Any other profile value is rejected;
there is no silent fallback. A no-FS catalog removes `Read`, `ListDir`, `Write`,
`Edit`, `Copy`, `Move`, `Remove`, `Grep`, `Glob`, `Shell`, `ShellStatus`,
`Parallel`, and `SkillDraft`. It retains web tools, memory, MCP tools, skills,
`Subagent`, and `Team`; children use the same file-less tool set and cannot
create a shell or fork a workspace.

The profile is fixed at session creation. The model cannot switch it during a
run. See [Core tools](/building/what-you-get/core-tools.md) for the complete
catalog.

## Child environments

Different delegation modes use different environment strategies:

- **Read-only Subagent and team members** get a child environment. When the
  workspace is trusted, the harness can use a worktree so child changes are
  isolated. An untrusted workspace withholds the read-only child shell because
  creating a worktree requires operating on the repository's `.git`.
- **Parallel branches** use hardened force-copy environments. The initial copy
  does not invoke Git; each branch then works in its own copied namespace.
- **Mutating members and direct-write Subagents** use the parent environment and
  modify the real workspace. They are serialized against sibling tool calls;
  there is no merge-back step.

All agent-facing shells use a secret-scrubbed environment. Provider keys,
`MECATL_*` credentials, cloud credentials, and other secret-shaped variables are
removed before a command runs.

## Persistence and reattachment

The environment identity is persisted with a session snapshot. Mecatl
reconstructs local and no-FS environments. A non-local identity can be
reattached only when the deployment supplies an `EnvironmentResolver`; a missing
resolver, mismatched identity, or nil workspace returns an error instead of
using a local workspace.

## Limitations

- No-FS sessions cannot use local file tools, shell commands, workspace forks,
  or parallel branches.
- Read-only child shells depend on project trust; this is separate from the
  parent's ability to run its own shell.
- Direct-write children can leave partial edits if cancelled or interrupted; the
  parent workspace and Git are the rollback boundary.
- Remote environment reattachment requires an explicit resolver.

## Next steps

- [Background Shell](/building/what-you-get/core-tools.md#background-commands)
- [Subagents, teams, and parallel](/building/what-you-get/subagents-teams-parallel.md)
- [Workspace trust](/features/permissions-and-posture.md)
