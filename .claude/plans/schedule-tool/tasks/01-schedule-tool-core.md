---
id: 01-schedule-tool-core
title: Schedule tool core (engine/agent + engine/port.ScheduleManager)
blocked_by: []
status: done
branch: "plan-schedule-tool/01-schedule-tool-core"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/schedule-tool
---

# Task brief

Implement the model-facing `Schedule` tool that the model calls in-chat to
manage scheduled tasks. This is the keystone task — every other task assumes a
working in-band create/list/fire verb.

**Placement (layering rule — read `AGENTS.md` "the layering rule" first).**
The tool lives in `engine/agent` (application layer, the Subagent/Team/Parallel
tool precedent). It consumes a NEW narrow consumer-local interface
`engine/port.ScheduleManager` that COMPOSITION satisfies with the existing
`internal/adapter/server.Service` schedule methods — the tool NEVER imports
`internal/adapter/server`, any adapter, proto, or gRPC. The injection precedent
is the Subagent tool's `WithSubagentStore(port.SessionStore)`: a
constructor/option that takes the port interface. Do NOT use the memory-tool
pattern (host adapter over `engine/tool.MemoryStore`) — this tool is an
application-layer orchestrator (it creates sessions and fires runs), so the
Subagent `engine/port` seam is the correct shape.

**`engine/port/schedule.go` — add `ScheduleManager`.** A consumer-local
interface exposing exactly the verbs the tool needs, satisfied by the existing
`Service` methods (`CreateSchedule`/`GetSchedule`/`ListSchedules`/
`UpdateSchedule`/`DeleteSchedule`/`PauseSchedule`/`ResumeSchedule`/`FireNow`/
`ListFires`). Keep it session+stdlib only, mirroring the existing port types in
that file. Widening the engine's exported surface → run `task api:update` and
add an `engine/CHANGELOG.md` note (Added = minor per `engine/COMPATIBILITY.md`).

**`engine/agent` — the `ScheduleTool`.** Verbs: `create`, `list`, `inspect`,
`pause`, `resume`, `delete`, `fire`. Map call args → the injected
`ScheduleManager`; render results as model-readable text (name, trigger, next
fire, enabled, last-fire stop reason, fire id + session id on `fire`).

**ReadOnly partition (the dispatch invariant — `AGENTS.md`).** `ReadOnly()`
must report `true` for the read-only verbs (`list`, `inspect`) and `false` for
the mutating verbs (`create`, `pause`, `resume`, `delete`, `fire`). Because a
single tool carries both, the cleanest shape is: the tool's `ReadOnly()` returns
based on the verb in the call — but `ReadOnly()` takes no args, so follow the
existing split precedent: either register two catalog entries (a read-only
`ScheduleQuery`-style tool for list/inspect and a mutating `Schedule` tool for
the rest), OR a single tool whose `ReadOnly()` is `false` (conservative: all
Schedule calls serialize). Pick the shape that keeps the AC1.4 partition test
green and honest — the AC asserts the read verbs are parallel-safe and the
mutating verbs serialize. Document the choice in the tool's doc comment.

**Composition wiring (`internal/app/catalog.go`).** Register the tool in the
catalog for every session whose store backs a `ScheduleStore`
(`scheduleStore() != nil`), the same conditional-registration shape as
`registerMemoryFamilies`. Add the `ScopeBuiltinDefault` floor Allow for the tool
name(s) in `defaultRules` (the memory-tool floor precedent at
`internal/app/build.go:6212-6217`) so it is pre-approved but config-overridable.

**Out of scope for THIS task (later tasks own it):** the cadence-floor
(`SchedulerMinInterval`) create-seam plumbing and the provider-selector
validation are Task 03; the on-by-default flip is Task 03; the settings/CLI
removal is Task 04; the prompt-affordance posture note + mutating-create
plan-mode gate is Task 05. This task lands the tool + registration + floor
Allow + the create/list/inspect/pause/resume/delete/fire verbs working against
the EXISTING create-seam (unchanged validation).

## Acceptance criteria

- AC1.1: A `Schedule` tool is present in the catalog of a session backed by a
  `ScheduleStore`, and absent (honest, not a stub) on a store with no
  `ScheduleStore` — `ServerCapabilities.Scheduling` and the tool registration
  agree.
  - verify: `TestScheduleTool_RegisteredOnlyWhenStoreBacked`
- AC1.4: The read-only verbs (`list`, `inspect`) report `ReadOnly()==true` and the
  mutating verbs (`create`, `pause`, `resume`, `delete`, `fire`) report
  `ReadOnly()==false`, so a mutating Schedule call never runs concurrently with a
  sibling read.
  - verify: `TestScheduleTool_ReadOnlyPartition`
- AC1.5: `Schedule fire <name>` returns the fire id + session id of the minted
  `sched--` session (the synchronous-to-terminal FireNow seam), and
  `Schedule list`/`inspect` surface the fire's terminal stop reason.
  - verify: `TestScheduleTool_FireAndInspectRoundTrip`
- AC1.5b: `Schedule fire <name>` on a schedule with a fire already in flight
  returns the singleton-overlap error (the same `ErrFireNowOverlap` the REST
  `FireNow` returns), not a second concurrent fire — the create-seam's
  default-true `Singleton` holds through the tool.
  - verify: `TestScheduleTool_FireOverlapRejected`
- AC1.6: A schedule created via the tool is visible, pausable, resumable, and
  deletable through the SAME store the REST/gRPC surface reads (one store, one
  truth — no tool-specific shadow state).
  - verify: `TestScheduleTool_SharesStoreWithRESTSurface`
