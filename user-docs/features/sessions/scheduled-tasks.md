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
next slot. By default a one-shot schedule can be lost. Opt-in retry can repeat the work,
so tasks using it must tolerate duplicate effects; use an external job system
when you need stronger delivery guarantees. Each successful claim creates a
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

Placement is resolved once when the schedule is created and every fire exactly reattaches
that durable placement. An in-chat schedule borrows its originating session's worktree;
an independent schedule owns a separately provisioned placement when the deployment provider
supports that lifecycle (including `microvm-local`). Updates cannot change either relationship.
Deleting a borrowed schedule leaves its origin untouched. Deleting an independently placed
schedule first persists an atomic, restart-safe deletion marker; claimed/running fires must settle
before that transition, and inspect/list report deletion pending until conditional completion succeeds.
Before the first claim, deletion cleans the owned placement while preserving dirty state. The first
atomic claim hands placement lifetime to the fire-session lineage; after it, deleting the schedule
removes only its record and retains the clean or dirty worktree for historical and resumable fire
sessions. Deletion never destroys a shared repository VM, rootfs, or sibling worktree. Legacy records
with ambiguous ownership keep the prior direct-delete behavior and are never guessed to be owned.

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

### Missed slots and overlap

The default misfire policy fires once immediately for a missed recurring slot,
then resumes the cadence. It does not replay every missed occurrence. Choose
`skip` when a stale run would be misleading.

New schedules suppress overlapping fires by default. The prior fire's session
lease determines whether it is still active across replicas. A released or
expired lease permits later work; a stale session pointer alone does not block
the schedule forever. Manual firing also rejects an overlapping active fire.

### One-shot retry

`one_shot_retry` is off by default. Enable it for a one-shot task that can safely
run more than once: the scheduler can re-arm a fire lost in a crash or ending in
an error. `one_shot_max_retries` defaults to three when retry is enabled and no
budget is supplied. The durable retry counter limits re-arming across restarts.
Recurring schedules reject this setting and use their misfire policy instead.

A re-armed one-shot starts with fresh context, even when carried context is enabled.
Retry is at least once within its configured budget, not an exactly-once guarantee.

### Carried context

`carry_context` is off by default. When enabled, Mecatl loads the prior fire's
conversation and includes it as fenced untrusted data in the new prompt. It is
background information, not replayed conversation or fresh authority. If the prior
session cannot be loaded, the fire continues with fresh context and a diagnostic.
Persist durable task state in memory or files when later runs must reload it.

### Deadlines and orphaned fires

The supplied deployment uses a 30-minute fire deadline unless the schedule supplies
its own nonzero `fire_timeout`. A deadline ends the run in a recoverable timeout
state. Inspect the fire before deciding whether to continue it manually.

The scheduler reconciles its latest orphaned in-flight fire after a crash. A claim
lost before session creation becomes an error fire record; a fire lost during its
run becomes an error record with a cancelled, recoverable session. Failed
reconciliation is retried on later ticks. This cleanup records what happened; it
is separate from opt-in one-shot retry. Older orphaned fire sessions beyond the
latest schedule state are not covered by this sweep.

### Delivery to the originating conversation

Schedules created in a conversation can deliver start and completion notes back
to that origin when the deployment supplies a delivery queue and the origin
remains authorized. A busy origin drains queued notes at a turn boundary; an
origin waiting for approval drains them when its approval pause resumes. An idle
or terminal origin can start a delivery turn. Delivery failure does not fail the
recorded fire: its result remains available for inspection.

Notes treat scheduled output as untrusted data and preserve
the origin's permission policy. Missing, unauthorized, or short-lived child
origins fall back to inspecting the fire record. There is no external webhook
callback guarantee.

## Limitations

- Every fire starts a separate session; optional carried context supplies only
  fenced background data.
- Fire records remain the inspection source even when origin-conversation
  delivery is enabled.
- A shareable multi-replica store needs a suitable lease and its own durability,
  authentication, and TLS configuration.
- Retained fire sessions and event logs may contain sensitive plaintext; protect
  the backing store accordingly.

## Next steps

- [Session continuity](/features/sessions/session-continuity.md)
- [Mecatl deployment choices](/operating/index.md)
- [Scheduled task API reference](/reference/grpc-api.md)
