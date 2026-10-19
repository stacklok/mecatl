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

A crash after a claim can skip that slot. Once the prior fire is reconciled,
recurring schedules continue at the next slot. By default a one-shot schedule can be lost. Opt-in retry can repeat
the work, so tasks using it must tolerate duplicate effects; use an external job
system when you need stronger delivery guarantees. Each successful claim creates
a fresh session whose record preserves the conversation, tool calls, usage,
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

A read-leaning schedule must set `mode` to plan. For a mutating schedule, the
fire's permission mode depends on how you created it:

- The in-chat `Schedule` tool records the mode its creating session had.
- A gRPC or REST request uses its `mode` field. When `mode` is unspecified, as
  in the `mecatui` overlay, each fire uses the server's default session mode.

Every fire checks its mode against the server ceiling again. If the server has
since restarted with a lower ceiling, the fire fails without running.

For an independently created schedule, the server selects placement from its
configured default. The create request has no workspace field. Use the HTTP
listener for this REST workflow:

```sh
curl -X POST http://127.0.0.1:8081/v1/schedules \
  -H 'content-type: application/json' \
  -d '{"name":"nightly-report",
       "prompt":"summarize commits from today",
       "mode":"PERMISSION_MODE_PLAN",
       "trigger":{"cron":"0 9 * * *"}}'

curl -X POST http://127.0.0.1:8081/v1/schedules/nightly-report/fire
curl http://127.0.0.1:8081/v1/schedules/nightly-report/fires/<FIRE_ID>
```

The REST routes are under `/v1/schedules`; the gRPC service is
`mecatl.v1.ScheduleService`. See the
[gRPC API reference](/reference/grpc-api.md) and the
[schedule proto](https://github.com/stacklok/mecatl/blob/main/contracts/proto/mecatl/v1/schedule.proto)
for exact request and response fields.

The `mecatui` overlay is available when the connected server advertises a
reachable schedule store.

## Manage schedules in Studio

[Studio](/operating/studio.md) lists every schedule you can see, with a detail
page whose **Run log** lists past runs. **View transcript** on a run opens that
run's saved transcript, read-only, without leaving the page. When you can't
view that session's transcript, the dialog shows the reason instead.

- **Filter and search.** Press `/` to search by name, prompt, or cadence.
  **Scheduled** lists schedules that are running, claimed, or waiting for their
  next run, and **Paused** lists paused ones. A finished schedule appears only
  under **All**.
- **Create and edit.** The dialog sets the prompt, a cron or one-time trigger,
  whether the task may make changes, and the tool profile. A new cron schedule
  uses your browser's time zone. Studio has no control for the time zone, run
  limit, or one-shot retry; use the API or the `Schedule` tool to set them.
- **What you can't change.** After creation, the name, trigger, and tool
  profile are fixed. To change when a task runs, create a new schedule and
  delete the old one. A one-time schedule whose time has passed can't be
  edited.
- **Finished schedules.** A one-time schedule that has run, or a cron schedule
  that reached its run limit, can be deleted but not run, paused, or resumed.

## Placement and deletion

Mecatl resolves placement when it creates the schedule. Every fire reattaches
that exact durable placement. An in-chat schedule borrows its originating
session's worktree; an independent schedule owns a separately provisioned
placement when the deployment provider supports that lifecycle (including
`microvm-local`). Updates cannot change either relationship.

On `mecak8s` with remote execution, workspace-backed schedules are limited to
in-chat schedules that borrow the origin's Kubernetes workspace. Each fire
receives its own session binding to that workspace, including across server
and execution-provider restarts. Independent remote workspace allocation is
unsupported and fails before allocating an environment; independent no-FS
schedules remain available. A fire fails while another run holds the borrowed
workspace, and a claimed slot is not retried unless its trigger's retry policy
allows it.

The original session must remain stored with the same workspace binding for
future fires. Deleting it, including through session retention, makes future
fires fail even if retained fire sessions keep the workspace alive. Updates
cannot retarget the schedule to another session. Create a new in-chat schedule
from a surviving session eligible to use that workspace instead.

Each retained fire session consumes one of the environment's 64 reference
slots, shared with the origin and other sessions. Configure fire-session
retention or delete unneeded fire sessions before that limit blocks further
fires. Deleting only the schedule does not release those references. Existing
sessions keep their persisted tool authority; start a new session if the
`Schedule` tool is absent from an older session's authority.

Deleting a borrowed schedule leaves its origin untouched. Deleting an
independently placed schedule first persists an atomic, restart-safe deletion
marker; claimed/running fires must settle before that transition, and
inspect/list report deletion pending until conditional completion succeeds.

Before the first claim, deletion cleans the owned placement while preserving
dirty state. The first atomic claim hands placement lifetime to the fire-session
lineage; after it, deleting the schedule removes only its record and retains the
clean or dirty worktree for historical and resumable fire sessions. Deletion
never destroys a shared repository VM, rootfs, or sibling worktree. Legacy
records with ambiguous ownership keep the prior direct-delete behavior and are
never guessed to be owned.

## Automatic firing

The automatic tick loop runs by default when the configured store supports
`ScheduleStore`. Use `--no-scheduler` to disable firing while keeping create,
list, inspect, pause, resume, and delete operations available. Manual fires
also stop and report `scheduler_not_running`.

Important operator settings include:

|Flag|Default|Purpose|
|-|-|-|
|`--no-scheduler`|`false`|Disable firing; schedule management remains available.|
|`--scheduler-tick-interval`|`30s`|Poll interval for due schedules.|
|`--scheduler-min-interval`|`1m`|Minimum recurring cadence; `0` disables the floor.|
|`--scheduler-max-concurrent-fires`|`4`|Maximum admitted fires, including manual fires and teardown.|
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

The scheduler holds a same-schedule execution fence until the fire and its
cleanup finish. Manual firing rejects an overlapping admitted fire. Across
replicas, an in-flight predecessor must be reconciled before another fire can
start; a lease lookup failure keeps that predecessor fenced.

### One-shot retry

`one_shot_retry` is off by default. Enable it for a one-shot task that can
safely run more than once: the scheduler can re-arm a fire lost in a crash or
ending in an error. `one_shot_max_retries` defaults to three when retry is
enabled and no budget is supplied. The durable retry counter limits re-arming
across restarts. Recurring schedules reject this setting and use their misfire
policy instead.

A re-armed one-shot starts with fresh context, even when carried context is
enabled. Retry is at least once within its configured budget, not an
exactly-once guarantee. Mecatl leaves a one-shot disabled when it cannot verify
that its current terminal outcome is an error.

### Carried context

`carry_context` is off by default. When enabled, Mecatl loads the prior fire's
conversation and includes it as fenced untrusted data in the new prompt. It is
background information, not replayed conversation or fresh authority. If the
prior session cannot be loaded, the fire continues with fresh context and a
diagnostic. Persist durable task state in memory or files when later runs must
reload it.

### Deadlines and orphaned fires

The supplied deployment uses a 30-minute fire deadline unless the schedule
supplies its own nonzero `fire_timeout`. The deadline requests cancellation.
A cooperative run finishes with a recoverable timeout result. An uncooperative
run remains in-flight with its saved deadline and last-progress time; inspection
shows **overdue/degraded; termination unconfirmed**. Diagnostics report this
state before attempting cancellation, including when cancellation blocks or fails.

Polling continues while a fire is stuck. Unrelated schedules can start within
remaining capacity. The stuck fire retains its execution ownership and capacity
slot until the run and cancellation worker finish; when all slots are occupied,
diagnostics report exhausted capacity and due work remains unclaimed. A healthy
leadership lease alone does not establish fire progress. Completion notes reach
the originating conversation only after the fire actually finishes.

After a crash, the scheduler reconciles its latest orphaned in-flight fire when
its session lease is free, recording an error and a recoverable cancelled
session. Failed reconciliation is retried on later ticks. A claim that has no
session record has no session lease to check: with a lease backend,
it stays quarantined and emits an ownership-unknown diagnostic. Investigate and
confirm that its former execution has stopped before arranging replacement work.
Without a lease backend, single-replica affinity permits reconciliation of that
claim after the stale window.
This cleanup is separate from opt-in one-shot retry. Older orphaned sessions
beyond the latest schedule state are not covered by the sweep.

### Delivery to the originating conversation

Schedules created in a conversation can deliver start and completion notes back
to that origin when the deployment supplies a delivery queue and the origin
remains authorized. A busy origin drains queued notes at a turn boundary; an
origin waiting for approval drains them when its approval pause resumes. An idle
or terminal origin can start a delivery turn. Delivery failure does not fail the
recorded fire: its result remains available for inspection.

Notes treat scheduled output as untrusted data and preserve the origin's
permission policy. Missing, unauthorized, or short-lived child origins fall back
to inspecting the fire record. There is no external webhook callback guarantee.

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
