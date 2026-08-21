---
sidebar_position: 8
title: Background Bash
description: Run bounded background shell work and inspect its status without blocking the agent loop.
---

# Background Bash

`Bash` is a mutating tool, but a call can set `background: true` when a command
needs to keep running while the agent continues. Typical uses include a local
dev server, a watcher, or a slow build.

## Availability

Background Bash is available wherever the normal `Bash` tool is available. It is
not present in a `no-fs` session, and it is not available to read-only children
when project trust withholds their shell.

## Start a job

Ask Bash to run in the background:

```json
{
  "command": "task dev",
  "background": true
}
```

The call returns immediately with a `bashcmd-<id>` job identifier. Permission is
checked once, before the command starts, using the same policy as a foreground
Bash call. Starting a background job does not make Bash read-only and does not
bypass approval.

The command runs in the **real session workspace**, not an isolated child. Its
filesystem effects can therefore interleave with the agent's `Edit`, `Write`, or
other commands. Use an isolated Subagent or Parallel branch when the work must
not touch the parent tree.

## Inspect and collect

`BashStatus` is the only model-facing status channel for background jobs:

| Call | Behavior |
| --- | --- |
| no arguments | List this run's jobs, their IDs, state, and stop reason |
| `{job_id: "..."}` | Show the command and retained output; collect a finished result |
| `{wait_ms: 5000}` | Wait up to the bounded interval for a job to finish |
| `{cancel: "..."}` | Request cancellation of a running job |

A finished result is delivered once. A running job exposes only a bounded tail of
recent output, so background Bash is not a durable log or an unbounded pipe.
Poll with `BashStatus` when the command's result matters; do not assume that
starting the process means it completed successfully.

## Lifecycle

Background jobs are scoped to the current run. They do not continue across
sessions or process restarts. At run end, any job still running is cancelled
automatically and the harness waits for it to stop. The job's output is not
silently appended to the parent conversation; collect it through `BashStatus`.

If a client disconnects or cancels the run, the run's cancellation and drain
logic also applies to its background jobs. A job that modifies files may leave
partial changes, so inspect the workspace and use version-aware file operations
or Git to recover safely.

## Security and limits

- The command is permission-checked before launch; `background: true` is not an
  approval mechanism.
- Agent-facing command environments are secret-scrubbed. Do not rely on provider
  credentials being available to the process.
- Background Bash has no workspace isolation and should not be used for
  untrusted repository code unless the deployment's shell posture permits it.
- Output is bounded to prevent a noisy process from consuming the run's memory.
- The job registry is run-scoped; a later prompt cannot inspect an old run's job.
- `Bash` remains mutating even for commands that happen to be read-only. Use
  `Grep` and `Glob` for read-only searches that can safely run in parallel.

## Troubleshooting

If a job appears stuck, call `BashStatus` with its `job_id` and a bounded
`wait_ms`, then cancel it if appropriate. If the run has ended, the job has been
cancelled by design. If the command needs to survive the run, move it outside
agent-controlled Bash and manage it with the deployment's process supervisor.

## Next steps

- [Execution environments](./execution-environments.md)
- [Subagents, teams, and parallel](../what-you-get/subagents-teams-parallel.md)
- [Core tools](../what-you-get/core-tools.md)
- [Capability and deployment matrix](./capability-matrix.md)
