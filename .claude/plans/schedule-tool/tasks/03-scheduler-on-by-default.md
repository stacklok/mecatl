---
id: 03-scheduler-on-by-default
title: Scheduler on by default + cadence floor + selector validation + flag flip
blocked_by: [01-schedule-tool-core]
status: done
branch: "plan-schedule-tool/03-scheduler-on-by-default"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/schedule-tool
---

# Task brief

Flip the scheduler to ON by default on any schedule-capable store, delete the
`--scheduler` opt-in flag (clean removal, no deprecated alias), and land the two
NEW create-seam validations the on-by-default + in-chat-create posture needs.

**On by default (`internal/app/build.go` `buildScheduler`, `cmd/mecated/main.go`,
`cmd/mecak8s/flags.go`).** `SchedulerEnabled` defaults to true whenever the
configured store exposes a `ScheduleStore` (jsonlstore `--store-dir`, redisstore
`--redis-url`, or a driver store that implements the accessor). A store with no
`ScheduleStore` (the in-memory default: mecademo, mecatequi, offline tests)
stays on the byte-identical no-scheduling path — `buildScheduler` returns
`(nil, noop, nil)`, no tick goroutine, `ServerCapabilities.Scheduling` false,
the `Schedule` tool absent. Note the current `buildScheduler` FAILS LOUD when
enabled-but-no-store; with on-by-default that becomes: no-store → silently
inert (byte-identical), store-backed → ticks. Reconcile this so the
default-on path never fails startup on an in-memory store.

**Flag flip.** Delete `--scheduler` and add `--no-scheduler` (mecak8s keeps its
own equivalent disable knob). Clean removal: `--scheduler` fails fast as an
unknown flag — NO deprecated alias, NO no-op shim (the user's explicit
instruction: "do not leave deprecated flags and no-ops, clean it all"). Update
the flag help text. The `--schedule-fire-retention` 7d default (currently keyed
on `cfg.schedulerEnabled`) now activates on the default path too.

**Cadence floor (`SchedulerMinInterval`).** It is currently INERT
(`cmd/mecated/main.go` documents "Phase 1 has no create API"). Thread it into
the SHARED create-seam (`validateScheduleSpec`/`applyScheduleDefaults` in
`internal/adapter/server/schedule.go`, currently signature-absent) so a cadence
tighter than the floor is rejected fail-closed — for BOTH the tool's create AND
the REST handler. The value flows from `scheduler.Config.MinInterval` / app
`Config.SchedulerMinInterval` into the Service create path.

**Provider-selector validation.** `validateScheduleSpec` does not validate
`Spec.Selector` (ProviderID/ModelID). Add a create-seam rejection of an
unknown/uncatalogued provider+model selector (fail-closed, like an invalid
cron), so a schedule fire cannot silently target a provider the deployment
never configured. This is in the SHARED seam so the tool and REST both inherit
it. Resolve the selector against the same provider catalog composition uses;
an empty selector (deployment default) is always valid.

## Acceptance criteria

- AC1.2c: `Schedule create` rejects an unknown/uncatalogued provider+model
  selector at create time (fail-closed, like an invalid cron) — a schedule fire
  must not silently target a provider the deployment never configured, surfacing
  hours later as a fire-time failure.
  - verify: `TestScheduleTool_CreateRejectsUnknownSelector`
- AC1.3: `Schedule create` rejects a cadence tighter than the configured
  `SchedulerMinInterval` frequency floor (the floor is now CONSULTED at the
  in-band create verb, no longer inert).
  - verify: `TestScheduleTool_CreateEnforcesMinInterval`
- AC2.1: With a durable (`--store-dir`) store and no flag, the scheduler ticks
  and a due schedule fires WITHOUT `--scheduler` being passed.
  - verify: `TestScheduleTool_SchedulerOnByDefault`
- AC2.1b: With a durable store and no retention flag, a `sched--` fire session
  older than 7d is swept by the `ScheduleFireRetention` GC pass (the 7d default
  now activates without `--scheduler`); an explicit `--schedule-fire-retention=0`
  disables it.
  - verify: `TestScheduleTool_FireRetentionDefaultActiveOnDefaultPath`
- AC2.2: `--no-scheduler` restores the pre-change behaviour (no tick loop, no
  auto-fire) on a schedule-capable store; the create/list/fire API still works
  (manual management is independent of the tick loop).
  - verify: `TestScheduleTool_NoSchedulerDisablesTickOnly`
- AC2.3: With the in-memory store (no `ScheduleStore`), startup neither ticks
  nor fails, `ServerCapabilities.Scheduling` is false, and the `Schedule` tool
  is absent — the byte-identical default.
  - verify: `TestScheduleTool_InMemoryStoreByteIdentical`
- AC2.5: The old `--scheduler` opt-in flag is DELETED outright (no deprecated
  alias, no no-op): `mecated --scheduler` fails fast with the standard
  unknown-flag startup error. The migration note lives in the changelog/docs,
  not in kept code.
  - verify: `TestScheduleTool_SchedulerFlagRemoved`
