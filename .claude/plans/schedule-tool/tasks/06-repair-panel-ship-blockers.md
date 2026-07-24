---
id: 06-repair-panel-ship-blockers
title: Repair wave — panel ship-blockers (AC1.4 partition, stale docs, posture defaults)
blocked_by: [05-schedule-posture-affordance]
status: done
branch: "plan-schedule-tool/06-repair-panel-ship-blockers"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/schedule-tool
---

# Task brief

Repair wave from the inline panel-review on the assembled accumulator. Fixes
the ship-blockers + the accepted security-default judgement calls. You are
running SOLO. All five original tasks (01–05) are merged.

**1. AC1.4 plan/code deviation (ship-blocker).** The plan's AC1.4 asserts the
read-only verbs (`list`, `inspect`) report `ReadOnly()==true` and the mutating
verbs report `false`. The implementation (`engine/agent/scheduletool.go`)
reports `ReadOnly()==false` for ALL verbs (the conservative choice — a single
tool's `ReadOnly()` takes no args). The named test
`TestScheduleTool_ReadOnlyPartition` only asserts `false`. Resolve the
deviation honestly: implement the per-verb partition so `list`/`inspect` are
parallel-safe and the mutating verbs serialize — the cleanest shape is
splitting into a read-only query tool (list/inspect, `ReadOnly()==true`) and a
mutating tool (create/pause/resume/delete/fire, `ReadOnly()==false`), the
pattern the plan's Work already sanctions. Update
`TestScheduleTool_ReadOnlyPartition` to assert the real per-verb split (watch
it go red first). If a single-tool split is genuinely impossible, report a
mis-decomposition rather than shipping a test that doesn't pin the AC.

**2. Stale living docs (ship-blocker).** `docs/usage.md` (~lines 60-180),
`docs/architecture.md` (~lines 460-471), and
`docs/design/IMPLEMENTATION-NOTES.md` (~lines 4413-4450) still describe the
REMOVED operator-tier `settings.yaml` `schedules:` block, the declarative
reconcile (`foldOperatorSchedules`/`reconcileSchedules`), and the `mecated
schedules` CLI in the present tense. Scrub them: remove the declarative/CLI
sections and the citations to deleted symbols; the management-surface paragraph
should name ONLY the surviving surfaces (the in-chat `Schedule` tool, the gRPC
`ScheduleService` + REST `/v1/schedules` API, the mecatui `/schedule` overlay).
Keep the on-by-default / `--no-scheduler` docs that task 03 correctly added.
Run `task docs` offline (matlatl from the module cache, `GOPROXY=off`; the
`go run ...@version` fetch fails offline) — llms.txt regen + `matlatl check .
--strict` must be green.

**3. Cadence floor default (security default).** `--scheduler-min-interval`
defaults to 0 (off), so under on-by-default + the floor-Allow Schedule tool a
model can mint an unbounded tight-cadence recurring fire. Default the floor to
a non-zero sane value (e.g. `1m`) so autonomous recurrence is bounded out of
the box; an operator can still set it explicitly (including back to a tighter
value, or 0 to disable the floor). Update the flag help + the AC2.1b/AC1.3
tests to reflect the new default.

**4. Plan-mode `fire` verb gate (LOW security).** `schedulePlanModeDeny`
(`engine/agent/scheduletool.go`) only blocks `create`+`mutating:true`; the
`fire` verb passes through, so a plan-mode session can trigger a pre-existing
`mutating:true` schedule's fire. Extend the plan-aware gate to also deny `fire`
of a mutating schedule in plan mode (consistent with the plan-mode
hard-deny-on-mutations invariant).

**Note on #5 (floor-Allow for create/fire):** the reviewer flagged the
`ScopeBuiltinDefault` floor-Allow covering `create`/`fire` as over-permissioned.
This is a deliberate design decision (the memory-tool floor precedent; the
fire's own mutations are still independently gated). Do NOT change the floor
scope in this wave — it stays floor-Allow. If you believe it must change,
report it as an open question rather than silently changing it.

## Acceptance criteria

- AC1.4 (re-pinned): The read-only verbs (`list`, `inspect`) report
  `ReadOnly()==true` and the mutating verbs (`create`, `pause`, `resume`,
  `delete`, `fire`) report `ReadOnly()==false`, so a mutating Schedule call
  never runs concurrently with a sibling read.
  - verify: `TestScheduleTool_ReadOnlyPartition`
- AC3.1/AC3.2 (doc consistency): the living docs no longer describe the removed
  settings block / CLI as present.
  - verify: inspection — `matlatl check . --strict` green + a grep for the
    removed surfaces returns no present-tense references.
- AC1.3 (default floor): the cadence floor defaults to a non-zero value and is
  enforced at create.
  - verify: `TestScheduleTool_CreateEnforcesMinInterval`
- AC4.3 (extended): plan-mode also denies `fire` of a mutating schedule.
  - verify: `TestScheduleTool_MutatingCreateGatedByPlanMode`
