---
id: 05-schedule-posture-affordance
title: Schedule tool model-visible affordance + floor/posture gates + full in-chat flow
blocked_by: [01-schedule-tool-core, 02-schedule-tool-validation, 03-scheduler-on-by-default, 04-remove-declarative-cli]
status: done
branch: "plan-schedule-tool/05-schedule-posture-affordance"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/schedule-tool
---

# Task brief

Harden the `Schedule` tool: the model-visible prompt instruction (ADR 0070),
the floor-scoped permission posture across all tiers, the mutating-create
plan-mode gate, the mecatui embedded-scheduler default, and the end-to-end
in-chat scenario test.

**Model-visible affordance (ADR 0070 — the model-visible-affordance-gate rule
in `AGENTS.md`).** A model-facing affordance that depends on the model's
behaviour is incomplete without (a) a prompt-layer instruction telling the
model the tool exists + the exact expected use, and (b) a test proving that
instruction lands in the BUILT engine's system prompt via the REAL factory
path. Add a `schedulePostureNote` constant + an `applySchedulePosture` helper
in `internal/app/build.go` (the `applyNoFSPosture`/`applyPlanModePosture`
idiom at `internal/app/build.go:5816-5838` and the `applyPlanModePosture`
shape) that appends the Schedule-tool instruction to the main engine's Role
suffix, wired for every session that has the Schedule tool. Assert against
`req.System.StablePrefix` (the layer the note owns), not the combined
`Render()`.

**Floor-scoped permission posture.** The `Schedule` tool resolves as a
`ScopeBuiltinDefault` Allow (no ask) under the default `auto` posture AND
under `strict`/`trusted` (the floor Task 01 added in `defaultRules`), and
remains config-overridable — an operator deny binds it. Pin this across the
tiers.

**Mutating-create plan-mode gate.** A `mutating: true` create is gated by the
session's posture: in a plan-mode session the plan-mode hard-deny vetoes the
mutating create; a read-leaning (`mutating: false`) create is allowed in plan
mode. (A schedule CREATE does not itself mutate the workspace — the FIRE's
posture is pinned at create-time by the existing Mutating/Mode invariant.)

**mecatui embedded scheduler (AC2.4).** The mecatui embedded server
(`cmd/mecatui/main.go` `embeddedConfig`) currently never sets
`SchedulerEnabled`, so nothing auto-fires from the TUI. With the scheduler now
on-by-default (Task 03), the TUI's default per-workspace jsonlstore should
advertise `Scheduling` AND auto-fire with no flag. Wire/verify this — the
`/schedule` overlay and in-chat fires work out of the box in the TUI.

**Full in-chat flow (AC4.4).** An offline engine (mockllm) drives the model to
create a schedule, list it, and fire it, and the fire mints a `sched--` session
that runs to a terminal stop.

## Acceptance criteria

- AC2.4: mecatui's embedded server advertises `Scheduling` and auto-fires on its
  default per-workspace store (no flag), so the `/schedule` overlay and in-chat
  fires work out of the box.
  - verify: `TestScheduleTool_TuiEmbeddedSchedulerOn`
- AC4.1: The built engine's system prompt (`req.System.StablePrefix`) contains
  the Schedule-tool instruction via the REAL factory path, so deleting the
  wiring fails CI.
  - verify: `TestScheduleTool_EngineSystemPromptContainsScheduleContract`
- AC4.2: The `Schedule` tool resolves as a `ScopeBuiltinDefault` Allow (no ask)
  under the default `auto` posture AND under `strict`/`trusted`, and remains
  config-overridable (an operator deny binds it).
  - verify: `TestScheduleTool_FloorScopedAllowAllTiers`
- AC4.3: A `mutating: true` create in a plan-mode session is denied (the
  plan-mode hard-deny on mutations); a read-leaning (`mutating: false`) create
  is allowed in plan mode.
  - verify: `TestScheduleTool_MutatingCreateGatedByPlanMode`
- AC4.4: `TestScheduleTool_Scenario4_FullInChatFlow` — an offline engine drives
  the model to create a schedule, list it, and fire it, and the fire mints a
  `sched--` session that runs to a terminal stop.
  - verify: `TestScheduleTool_Scenario4_FullInChatFlow`
