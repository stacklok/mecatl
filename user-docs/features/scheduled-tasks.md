---
sidebar_position: 9
title: Scheduled tasks
description: Schedule recurring and one-shot mecatl runs with durable delivery and recovery.
---

# Scheduled tasks

Scheduled tasks run a saved prompt autonomously on a cron cadence or once at a
future time. Each fire is a fresh, bounded session. There is no human available
at fire time to approve a tool call, so the schedule pins its profile, limits,
permission mode, provider/model selection, and whether mutation is allowed.

Scheduled tasks are a composition-layer feature. The agent loop stays unaware of
the scheduler; composition claims a slot, creates a session, drives it through
the ordinary run-entry path, and records the outcome.

## Availability

Scheduling requires a backend that implements `ScheduleStore`:

- local JSONL storage from `mecated --store-dir`;
- Redis storage from `mecak8s --redis-url`;
- a remote schedule-store driver selected with `--schedule-store-url`; or
- an engine embedding that supplies the port.

The plain in-memory store has no durable scheduler. Without a schedule-capable
backend, the Schedule tool and schedule APIs report that the feature is
unavailable rather than pretending that schedules will survive.

## Create a schedule

Schedules can be managed from the in-chat `Schedule` tool, through gRPC, or
through the REST mirror. The management operations are create, list, inspect,
pause, resume, delete, and fire-now. There is no separate `mecated schedules`
CLI.

A schedule has one trigger: a cron expression or a one-shot timestamp. It also
pins a workspace/profile, permission mode, provider/model selector, per-fire
limits, timezone, and a `mutating` opt-in. The default is read-leaning; a
schedule that may use `Edit`, `Write`, or `Bash` must explicitly set
`mutating: true`. This is not an implicit yolo mode.

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

The REST routes are under `/v1/schedules`; the gRPC service is
`mecatl.v1.ScheduleService`. `FireNow` uses the same atomic claim path as an
ordinary due fire. The in-chat tool uses the same validated create seam, so a
schedule created in a conversation and one created over the wire behave the
same way.

## How firing works

The durable schedule store is the source of truth. Before a fire runs, the
scheduler atomically advances `NextFireAt`, increments the fire count, and
claims the slot. A second replica cannot claim the same slot. This is
**at-most-once** slot claiming without a distributed transaction.

A leader lease on the reserved scheduler identity (`__scheduler__`) normally
allows only one replica to poll the store. The lease prevents duplicate polling
and wasted work, but the durable claim is the correctness fence. If no suitable
lease backend exists, the scheduler reports the single-replica limitation rather
than silently claiming multi-replica safety.

Recurring schedules apply a misfire policy:

- `fire_once_now` (the default) runs once for missed time and resumes the normal
  cadence; it does not replay every missed interval;
- `skip` advances past the missed slot without running it.

A schedule's total fire count can be bounded with `max_fires`; a one-shot fires
once by definition. The current create seam keeps same-schedule overlap
suppressed, so a still-running prior fire is skipped.

## Monitor and recover scheduled runs

Each fire creates a new top-level session. Its conversation, tool calls, usage,
and terminal state live in the session store. The schedule's fire record points
to that session and records the terminal stop or error. Results are pull-based:
use `GetFire`/`ListFires`, the REST fire routes, or the TUI `/schedule` overlay.

A fire is visible while running, with its claimed state, session, progress, and
deadline. Fires have a wall-clock timeout (30 minutes by default). A timeout is
distinct from manual cancellation and leaves the session recoverable. A process
shutdown or crash reconciles an orphaned in-flight fire instead of leaving a
permanent pending record.

Recurring schedules can self-heal after a missed slot through their misfire
policy. A one-shot may be lost if the process crashes after the claim and before
execution completes. That is the deliberate trade-off for at-most-once firing
without a distributed transaction. If the work cannot be lost, use an external
job system with its own delivery contract.

## Configuration

The main scheduler flags are:

| Flag | Default | Purpose |
| --- | --- | --- |
| `--no-scheduler` | `false` | Disable the automatic tick loop; manual create/list/fire management still works. |
| `--scheduler-tick-interval` | `30s` | Poll interval for due schedules. |
| `--scheduler-min-interval` | `1m` | Minimum recurring cadence enforced at creation; `0` disables the floor. |
| `--scheduler-max-concurrent-fires` | `4` | Maximum due fires processed in parallel per tick. |
| `--schedule-fire-retention` | `7d` when unset | Age retention for persisted fire sessions. Explicit `0` disables the age pass. |
| `--schedule-fire-retention-max-total` | `0` | Store-wide cap for retained fire sessions; zero disables the cap. |
| `--schedule-store-url` | empty | Independent remote schedule-store driver. |

The tick loop is on by default when the configured backend exposes a
`ScheduleStore`; `--no-scheduler` opts out of automatic polling but does not
remove the API or tool. A cadence below the configured minimum is rejected
fail-closed, including when requested through the model-facing Schedule tool.

## Limitations

- Every fire starts with fresh context. If later fires need continuity, persist
  the required state in memory or a file and include instructions to reload it.
- A scheduled fire is headless. Permission asks cannot wait for a human, so use a
  read-leaning mode or explicitly configure the required mutating posture.
- Fire delivery is pull-only in the current deployment surface; the schedule
  store does not push results to a caller.
- A long-running fire can delay polling and therefore increase the start latency
  of other due schedules, although claimed slots remain protected.
- Retention applies only to durable stores. Treat fire sessions and event logs as
  sensitive plaintext and protect their storage permissions.
- Remote schedule stores and multi-replica deployments require the backend's
  authentication, TLS, durability, and lease guarantees to be configured
  explicitly.

For the complete API, lifecycle events, shutdown behavior, and backend details,
see [Scheduled tasks](../what-you-get/scheduled-tasks.md) and the
[operator flag reference](https://github.com/stacklok/mecatl/blob/main/docs/usage/mecated.md).

## Next steps

- [Session continuity](./session-continuity.md)
- [Mecatl deployment choices](../getting-started/deployment-decision.md)
- [Scheduled task API](../deployment/grpc-http.md#scheduled-task-rpcs)
- [Capability and deployment matrix](./capability-matrix.md)
