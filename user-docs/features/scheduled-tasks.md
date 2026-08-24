---
sidebar_position: 9
title: Scheduled tasks
description: Schedule recurring and one-shot mecatl runs with durable delivery and recovery.
---

# Scheduled tasks

Scheduled tasks run a saved prompt autonomously on a cron cadence or once at a
future time. Each fire starts a fresh, bounded, headless session with its own
workspace, provider/model selection, permission mode, limits, and explicit
mutation setting.

The scheduler is a composition-layer feature. The detailed storage, claiming,
lease, firing, event, and recovery model is documented in [Scheduled tasks for
builders](/building/what-you-get/scheduled-tasks.md). This page focuses on using
and operating the feature.

## Availability

Scheduling requires a backend that implements `ScheduleStore`:

- local JSONL storage from `mecated --store-dir`;
- Redis storage from `mecak8s --redis-url`;
- a remote schedule-store driver selected with `--schedule-store-url`; or
- an engine embedding that supplies the port.

Without a schedule-capable backend, the Schedule tool and schedule APIs report
that scheduling is unavailable. The plain in-memory store does not provide
durable schedules.

## Create and manage a schedule

Schedules can be managed from the in-chat `Schedule` tool, through gRPC, or
through the REST API. The operations are create, list, inspect, pause, resume,
delete, and fire-now. There is no separate `mecated schedules` CLI.

A schedule uses exactly one trigger: a cron expression or a one-shot timestamp.
It defaults to read-leaning behavior. A schedule that may use `Edit`, `Write`,
or `Bash` must explicitly set `mutating: true`; this is not an implicit
allow-all mode.

Example REST workflow:

```console
curl -X POST http://localhost:8080/v1/schedules \
  -H 'content-type: application/json' \
  -d '{"name":"nightly-report",
       "prompt":"summarize commits from today",
       "workspace":"/repo",
       "mode":"PERMISSION_MODE_PLAN",
       "trigger":{"cron":"0 9 * * *"}}'

curl -X POST http://localhost:8080/v1/schedules/nightly-report/fire
curl http://localhost:8080/v1/schedules/nightly-report/fires/<fire-id>
```

The REST routes are under `/v1/schedules`. The gRPC service is
`mecatl.v1.ScheduleService`. See the [gRPC API reference](https://github.com/stacklok/mecatl/blob/main/docs/usage/grpc-api.md)
and the [schedule proto](https://github.com/stacklok/mecatl/blob/main/contracts/proto/mecatl/v1/schedule.proto)
for exact request and response fields.

In mecatui, the `/schedule` overlay provides the same management operations when
the connected server advertises a reachable schedule store.

## Automatic firing

The automatic tick loop runs by default when the configured store supports
`ScheduleStore`. Use `--no-scheduler` to disable automatic polling while keeping
manual create, list, inspect, and fire operations available.

Important operator settings include:

| Flag | Default | Purpose |
| --- | --- | --- |
| `--no-scheduler` | `false` | Disable automatic polling; manual management remains available. |
| `--scheduler-tick-interval` | `30s` | Poll interval for due schedules. |
| `--scheduler-min-interval` | `1m` | Minimum recurring cadence; `0` disables the floor. |
| `--scheduler-max-concurrent-fires` | `4` | Maximum due fires processed in parallel per tick. |
| `--schedule-fire-retention` | `7d` when unset | Retention for persisted fire sessions. |
| `--schedule-store-url` | empty | Remote schedule-store driver, independent of the session store. |

## Results and recovery

Each fire creates a new top-level session. Its conversation, tool calls, usage,
and terminal state are persisted when the session store is durable. The fire
record points to that session and records its terminal result. Use `GetFire`,
`ListFires`, the REST routes, or the mecatui `/schedule` overlay to inspect it.

Scheduled fires are headless: no person is available to approve a tool call. Use
a read-leaning mode or explicitly configure the permissions and mutation posture
the task requires.

A recurring schedule uses its configured misfire policy after a missed slot. A
one-shot may be lost if the process crashes after its slot is claimed and before
execution completes; use an external job system when that work cannot be lost.
A fire that times out or is interrupted remains recoverable through the normal
session lifecycle.

## Limitations

- Every fire starts with fresh context. Persist state in memory or files and
  instruct later prompts to reload it when continuity is needed.
- Results are pull-based in the current deployment: inspect the fire record or
  session rather than expecting a pushed callback.
- A shareable multi-replica store needs a suitable lease and its own durability,
  authentication, and TLS configuration.
- Retained fire sessions and event logs may contain sensitive plaintext; protect
  the backing store accordingly.

For the claim-before-fire, at-most-once, singleton, leader-lease, event, metrics,
and shutdown details, see [Scheduled tasks for builders](/building/what-you-get/scheduled-tasks.md).

## Next steps

- [Session continuity](./session-continuity.md)
- [Mecatl deployment choices](/building/getting-started/deployment-decision.md)
- [Scheduled task API reference](https://github.com/stacklok/mecatl/blob/main/docs/usage/grpc-api.md)
- [Capability and deployment matrix](./capability-matrix.md)
