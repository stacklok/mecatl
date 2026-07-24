---
id: 02-schedule-tool-validation
title: Schedule tool create-seam validation parity (cron/mode/workspace/Phase-2 fields)
blocked_by: [01-schedule-tool-core]
status: done
branch: "plan-schedule-tool/02-schedule-tool-validation"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/schedule-tool
---

# Task brief

Prove the `Schedule` tool's `create` verb rides the EXISTING create-seam
(`validateScheduleSpec` + `applyScheduleDefaults`,
`internal/adapter/server/schedule.go`) byte-for-byte — never a second,
drifted validation path. This is a small verification-and-hardening task on top
of the tool Task 01 lands; it owns the ACs that assert the create-seam's
existing invariants hold THROUGH the tool.

**Scope.** The tool Task 01 builds already calls `Service.CreateSchedule`. This
task adds the named pinning tests that the tool's `create` verb enforces the
create-seam's existing validation (cron grammar, the Mutating/Mode invariant,
the profile-aware workspace rule, and the Phase-2 field rules) exactly as the
REST `POST /v1/schedules` handler does. If Task 01's tool accidentally
duplicated or skipped part of the seam, these tests catch it; if the tool
genuinely reuses the seam, these tests are the pin. Where a check genuinely
needs the seam tightened (not duplicated), tighten the SHARED
`validateScheduleSpec`/`applyScheduleDefaults` so BOTH the tool and the REST
handler inherit it — never add a parallel check in the tool.

**Do NOT** implement the cadence floor (`SchedulerMinInterval`) or the
provider-selector validation here — those are NEW create-seam validations owned
by Task 03. This task pins only the validation that already exists in
`validateScheduleSpec` today.

## Acceptance criteria

- AC1.2: `Schedule create` with a cron expression saves an enabled schedule whose
  `NextFireAt` is the cronparse-computed first fire, and rejects (fail-closed) an
  invalid cron, a non-plan non-mutating mode, or an empty workspace on a
  default-profile schedule — the SAME validation the REST create-seam enforces.
  - verify: `TestScheduleTool_CreateValidatesLikeRESTSeam`
- AC1.2b: `Schedule create` enforces the create-seam's Phase-2 field rules by
  name: `oneShotRetry: true` on a cron trigger is rejected fail-closed (a cron
  self-heals via misfire), and a one-shot with `oneShotRetry: true` and no
  `maxRetries` gets the create-seam default of 3 — so the tool cannot be wired
  to a subset of the seam.
  - verify: `TestScheduleTool_CreateEnforcesPhase2FieldRules`
