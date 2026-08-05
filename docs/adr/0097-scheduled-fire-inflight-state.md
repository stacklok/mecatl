# ADR 0097 — Scheduled fires are observable in-flight: a first-class persisted lifecycle stage

- Status: Accepted
- Date: 2026-08-05
- Scope: `engine/port/schedule.go`, `engine/session`, `internal/adapter/scheduler`, `internal/app`, `internal/adapter/server`, `cmd/mecatui`
- Supersedes: none (extends [ADR 0059](./0059-scheduled-tasks.md))

## Context

A scheduled fire was invisible until it terminated. `ScheduleStore.Claim` advanced
`FireCount` and stored the `"pending"` sentinel in `LastFireSessionID`; only
`RecordFire` — called after the whole run reached a terminal result — wrote an
inspectable fire record. `renderScheduleInspect` rendered any claim-without-record
as `fires: none`, which conflated four states an operator cannot tell apart:
running normally, running but stuck, crashed after Claim, and a `RecordFire`
failure. Observed with `monitor-pr-6208`: the schedule triggered, `inspect` showed
`fire count: 1` and `fires: none`, the `sched--` session was still `running` with
1.2M tokens spent, and nothing was delivered back — the operator had to ask, then
inspect storage by hand (issue #386).

The missing concept is the **in-flight fire** as a distinct, *persisted* lifecycle
stage. An in-memory registry would not survive a restart — which is exactly the
state the crash-distinction criterion needs ("a crashed process… becomes
distinguishable from a live run after restart"). Fires also had turn/tool-call
bounds but no wall-clock bound, so a run could stay non-terminal and silent for a
long time.

## Decision

Promote the in-flight fire to a first-class persisted stage in the durable store,
and give each fire a wall-clock deadline.

- **State model** (`engine/port/schedule.go`): `ScheduleState` gains
  `LastFireStartedAt` (when the run actually began — distinct from `LastFireAt`,
  the Claim instant), `LastFireProgressAt` (last observed progress), and
  `FireDeadline`; `ScheduleFire` gains `StartedAt`/`ProgressAt`/`Deadline`;
  `ScheduleSpec` gains `FireTimeout` (the per-fire wall-clock knob; zero = the
  deployment default). `ScheduleStore` gains two methods: `RecordFireStart`
  (persist the in-flight fire — empty `Stop` — and stamp the real `sched--`
  session id + `LastFireStartedAt` *as soon as the fire starts*, not only after
  `RecordFire`) and `RecordFireProgress` (advance `LastFireProgressAt`).
  `RecordFire` flips the fire terminal and clears the in-flight state fields;
  `Claim`/`ClaimNow` zero them (a fresh claim has no start/progress yet).
- **Wall-clock deadline + `StopTimeout`** (`engine/session`): a `time.AfterFunc`
  watchdog in `makeFireFunc` cancels the in-flight run via the blessed
  `Service.Cancel`/`run.Cancel` seam after `effectiveFireDeadline`
  (`spec.FireTimeout`, else `defaultFireTimeout` = 30m). A deadline-fired run that
  drained to `StopCancelled` is overridden to the new `StopTimeout` reason — a
  clean, recoverable budget-exhaustion terminal (a `StopBudget` sibling), not a
  manual cancel — with an honest error naming the timeout. The session settles to
  a recoverable terminal snapshot.
- **Started notice** (`internal/app/scheduler_delivery_run.go`
  `deliverFireStarted`): a fenced-untrusted, harness-authored
  `[scheduled task <name> started (fire <id>)]` notice enqueued to the SAME
  `DeliveryQueue` exactly-once ledger as the terminal report (a distinct ledger
  entry, never a duplicate or a collision), state-aware (busy/awaiting origin
  enqueues only), carrying NO model-authored fire content.
- **Stale-fire reconciliation**: the scheduler *detects* stale in-flight state
  store-only (the `"pending"` sentinel past the window, or a real in-flight fire
  whose prior-fire lease is no longer live) and *reconciles* via a
  composition-injected `ReconcileStaleFire` callback — the scheduler stays
  storage-agnostic and never imports the server. Crash-after-Claim records a
  terminal `StopError` fire; crash-after-session settles the session to
  `cancelled` (Interrupt-recoverable) and records `StopError`. A genuinely-live
  fire (lease held) is never reconciled.
- **Surfaces agree**: `renderScheduleInspect` renders a claimed fire as
  `in-flight: claimed (session pending)` (never `fires: none`) and an in-flight
  fire with started/last-progress/deadline; the gRPC/HTTP wire projection and the
  TUI overlay carry the same fields and render the same lifecycle.

## Consequences

**Easier:** an operator can answer "did it trigger / which fire is running / is it
progressing / when was last progress / did it time out, crash, or complete" from
`inspect` (or the overlay, or the wire) without touching storage. A runaway fire
self-terminates at its deadline instead of burning tokens silently. A crashed fire
is recovered as a terminal record instead of sitting `pending` forever.

**Harder / accepted costs:**
- `ScheduleStore` widened by two methods — a breaking change for any *external*
  implementer, but every implementation is in-tree (memschedulestore, jsonlstore,
  redisstore) and moves in monorepo lockstep, so there is no external break. The
  api-compat gate + `engine/CHANGELOG.md` record it (Added/minor).
- One store write per turn boundary (`RecordFireProgress`). Bounded by the fire's
  existing `MaxTurns` cap; deliberately NOT per-chunk (the hot path).
- The started notice is a second delivery kind on the shared ledger — one more
  enqueue per fire, deduped by the same exactly-once mechanism.
- The stale-reconcile window is heuristic (a lease-liveness + age check); a very
  long-but-legitimate fire could in principle be misread as stale if its lease
  lapsed. Mitigated by requiring BOTH the lease released AND the start past the
  window, and by the per-fire `FireDeadline` shortening the ambiguous window.

## See also

- [ADR 0059 — Scheduled tasks](./0059-scheduled-tasks.md) (the subsystem this extends).
- [ADR 0075 — Fire-result delivery](./0075-fire-result-delivery.md) (the terminal
  delivery channel the started notice rides alongside).
- [ADR 0076 — The schedule manager is store-shaped](./0076-schedule-shared-catalog.md).
- [ADR 0027 — Cloud-native](./0027-cloud-native.md) (the durable-store discipline
  the in-flight state follows; the stale reconciler earns no new List-1 row).
- [ADR 0096 — Live-feed reconnect](./0096-live-feed-reconnect.md) (the client path
  that renders the delivery notes this produces).
- The lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
