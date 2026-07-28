---
id: 01-schedule-manager
title: Pre-Service store-shaped scheduleManager + Service delegation
blocked_by: []
status: done
branch: "plan-schedule-shared-catalog/01-schedule-manager"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/schedule-shared-catalog
---

# Task brief

Extract the schedule seam off `*server.Service` into a standalone, store-shaped
`scheduleManager` (new file `internal/adapter/server/schedule_manager.go`),
constructed from inputs available BEFORE `server.NewService`: the
`port.SessionStore` (from which the `ScheduleStore` is type-asserted via the
existing `scheduleStoreProvider` accessor), a now-func, the cadence floor
(atomic), the event-log, diagnostics, and the shared live-model-inventory
atomic pointer. The in-process scheduler (for `FireNow`) becomes a late-set
atomic field on the manager.

Move these OFF `*Service` onto the manager, VERBATIM (one seam, one truth):
the nine `port.ScheduleManager` methods (Create/Get/List/Update/Delete/Pause/
Resume/FireNow/ListFires), `validateScheduleSpec`, `validateCronTrigger`,
`validateScheduleOrigin`, `validateScheduleSelector`, `scheduleMinInterval`,
`EmitScheduleEvent`/`emitScheduleEvent`, and the `SetScheduler` /
`SetScheduleMinInterval` setters + `HasScheduler` read. The manager holds the
store directly (origin validation reads `store.Load`, not `s.cfg.Store.Load`).
Selector validation reads the live model inventory via the SAME
`*atomic.Pointer[[]*mecatlv1.ModelInfo]` the Service swaps in `SetModels` —
pass that pointer into the manager at construction so `SetModels` keeps
working with no second copy.

`*server.Service` keeps thin DELEGATING wrappers for the entire RPC surface
(`grpc_schedule.go`, the REST `/v1/schedules` handlers, the mecatui overlay) so
the wire is byte-identical. `Service.ScheduleManager()` returns the embedded
manager (the single truth), NOT `s`, when the store backs a `ScheduleStore`
(nil otherwise — the gate is unchanged). `ServerCapabilities.Scheduling` still
reads `scheduleStore() != nil`.

Composition (`internal/app/build.go`): construct the manager from the store
BEFORE `buildEngine`, hand it to `server.Config` (the Service consumes it
instead of self-discovering the store / memoising `scheduleStoreCache` — delete
the cache), and have `svc.SetScheduleMinInterval` / `svc.SetScheduler` delegate
to the manager. Do NOT yet change WHEN `assets.scheduleManagerFactory` is bound
(that is task 02); this task is a pure refactor — every existing schedule test
must stay green with zero behaviour change.

## Acceptance criteria

- AC1.1: The manager is constructable from a `port.SessionStore` + now-func
  alone — no `*server.Service` value is required. A store with no
  `ScheduleStore` yields a nil/absent manager (the honest no-scheduling path),
  matching `ServerCapabilities.Scheduling` ([ADR-0073](../adr/0073-schedule-tool.md)).
  - verify: `TestScheduleSharedCatalog_Scenario1_ManagerIsStoreShaped`
- AC1.2: The create-seam is byte-identical after the move: a valid cron saves
  ENABLED with the cronparse-computed first fire; an invalid cron, a
  non-plan non-mutating mode, an empty workspace on a default-profile
  schedule, a `one_shot_retry` cron, and an unknown provider+model selector
  are all rejected fail-closed with `ErrInvalidArgument` and nothing saved.
  - verify: `TestScheduleTool_CreateValidatesLikeRESTSeam`, `TestScheduleTool_CreateEnforcesPhase2FieldRules` (existing — must stay green against the moved seam)
- AC1.3: The Service's RPC surface delegates without behaviour change:
  `Service.ScheduleManager()` returns non-nil exactly when the store backs a
  `ScheduleStore`, and `CreateSchedule`/`GetSchedule`/`ListSchedules`/
  `UpdateSchedule`/`DeleteSchedule`/`PauseSchedule`/`ResumeSchedule`/`FireNow`/
  `ListFires`/`EmitScheduleEvent` behave identically through the delegating
  wrapper.
  - verify: `TestScheduleSharedCatalog_Scenario1_ServiceDelegates`
- AC1.4: `FireNow` still distinguishes its three states after the move —
  `ErrNoScheduleStore` (no store), `ErrSchedulerNotRunning` (store present, no
  scheduler wired), and the mapped scheduler sentinels — with the scheduler
  late-set onto the manager, not the Service.
  - verify: `TestScheduleSharedCatalog_Scenario1_FireNowStatesPreserved`
