---
slug: /features/scheduled-tasks
sidebar_position: 150
title: Scheduled tasks
description:
  Schedule recurring and one-shot Mecatl runs with durable delivery and
  recovery.
---

# Scheduled tasks

Schedule a saved prompt to run on a cron cadence or once at a future time. Each
fire starts a fresh headless session with its configured workspace, model,
permissions, limits, and mutation setting.

## Availability

Scheduling requires a backend that implements `ScheduleStore`:

- local JSONL storage from `mecated --store-dir`;
- Redis storage from `mecak8s --redis-url`;
- a remote schedule-store driver selected with `--schedule-store-url`; or
- an engine embedding that supplies the port.

Without a schedule-capable backend, the Schedule tool and schedule APIs report
that scheduling is unavailable. The plain in-memory store does not provide
durable schedules.

## Storage and delivery

The schedule store is authoritative. Each schedule contains an immutable prompt,
trigger, model selection, workspace, permission mode, limits, mutation setting,
and timezone, plus mutable fire state.

Mecatl claims and advances a due slot before starting its run. This prevents two
replicas from firing the same slot. A leader lease limits polling to one
replica, while the atomic claim remains the duplicate-execution safeguard.

A crash after a claim can skip that slot. Recurring schedules continue at the
next slot. A one-shot schedule can be lost, so use an external job system when
the work requires stronger delivery guarantees. Each successful claim creates a
fresh session whose record preserves the conversation, tool calls, usage,
terminal state, and fire result.

Embeddings can provide `port.ScheduleStore`. Implementations must make claims
atomic and can use the shared `engine/adapter/scheduleconformance` suite.

## Create and manage a schedule

Use the in-chat `Schedule` tool, gRPC, REST API, or `mecatui` `/schedule`
overlay to create, inspect, pause, resume, delete, or immediately fire a
schedule. Mecatl has no separate schedules CLI.

A schedule uses exactly one trigger: a cron expression or a one-shot timestamp.
It defaults to read-leaning behavior. A schedule that may use `Edit`, `Write`,
or `Shell` must explicitly set `mutating: true`; this is not an implicit
allow-all mode.

Example REST workflow:

```sh
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

The REST routes are under `/v1/schedules`; the gRPC service is
`mecatl.v1.ScheduleService`. See the
[gRPC API reference](/reference/grpc-api.md) and the
[schedule proto](https://github.com/stacklok/mecatl/blob/main/contracts/proto/mecatl/v1/schedule.proto)
for exact request and response fields.

The `mecatui` overlay is available when the connected server advertises a
reachable schedule store.

## Automatic firing

The automatic tick loop runs by default when the configured store supports
`ScheduleStore`. Use `--no-scheduler` to disable automatic polling while keeping
manual create, list, inspect, and fire operations available.

Important operator settings include:

|Flag|Default|Purpose|
|-|-|-|
|`--no-scheduler`|`false`|Disable automatic polling; manual management remains available.|
|`--scheduler-tick-interval`|`30s`|Poll interval for due schedules.|
|`--scheduler-min-interval`|`1m`|Minimum recurring cadence; `0` disables the floor.|
|`--scheduler-max-concurrent-fires`|`4`|Maximum due fires processed in parallel per tick.|
|`--schedule-fire-retention`|`7d` when unset|Retention for persisted fire sessions.|
|`--schedule-store-url`|empty|Remote schedule-store driver, independent of the session store.|

## Results and recovery

Each fire creates a top-level session. A durable store preserves its
conversation, tool calls, usage, and terminal state. Inspect the result with
`GetFire`, `ListFires`, the REST routes, or the `mecatui` overlay.

Scheduled fires are headless: no person is available to approve a tool call. Use
a read-leaning mode or explicitly configure the permissions and mutation posture
the task requires.

A recurring schedule uses its configured misfire policy after a missed slot. A
one-shot may be lost if the process crashes after its slot is claimed and before
execution completes; use an external job system when that work cannot be lost. A
fire that times out or is interrupted remains recoverable through the normal
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

## Next steps

- [Session continuity](/features/sessions/session-continuity.md)
- [Mecatl deployment choices](/operating/choose-deployment.md)
- [Scheduled task API reference](/reference/grpc-api.md)
