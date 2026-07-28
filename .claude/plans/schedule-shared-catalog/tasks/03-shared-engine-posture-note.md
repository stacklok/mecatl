---
id: 03-shared-engine-posture-note
title: Shared-engine schedule posture note + default-session e2e
blocked_by: [02-shared-catalog-registration]
status: done
branch: "plan-schedule-shared-catalog/03-shared-engine-posture-note"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/schedule-shared-catalog
---

# Task brief

Make the default store-backed session actually able to schedule end-to-end.
With task 02 the shared catalog carries the tools; the shared engine's Deps
ALREADY wire `OriginBinder` (`internal/app/build.go:2529`) and `DeliveryQueue`
(`build.go:2535`) from the assets — both populated by `registerScheduleTool`
once the manager is bound, so fire-result delivery
([ADR-0075](../adr/0075-fire-result-delivery.md)) needs NO new wiring.

What is missing is the PROMPT NOTE: the shared engine's PromptConfig never gets
`applySchedulePosture` (today applied only in the per-session factory at
`build.go:1941`). Apply it to the SHARED engine's deps (in `buildEngine`, where
`deps.PromptConfig` is set) under the SAME gate the factory uses
(`scheduleManagerPresent(assets)`), per the
[ADR-0070](../adr/0070-model-visible-affordance-gate.md) affordance gate — the
model must be told the tool exists.

Then prove the observable end-to-end: a default-profile, zero-selector,
store-backed session (the shared-engine fast path — a plain mecatui launch)
resolves the `Schedule` tool, has the posture note in its system prompt
(asserted against the `req.System.StablePrefix` layer, per ADR 0070 — not the
combined Render), and its origin/delivery wiring is live. Also pin the restart
path: a default-profile session persisted then reloaded via `needsRehydration`
still resolves the tool.

## Acceptance criteria

- AC3.1: A default-profile, zero-selector session on a store-backed `Build`
  resolves the `Schedule` tool from its engine's catalog (the shared-engine
  fast path — no per-session factory involved).
  - verify: `TestScheduleSharedCatalog_Scenario3_DefaultSessionHasTool`
- AC3.2: That session's built system prompt contains the schedule posture note
  (the `schedulePostureNote` "You have a Schedule tool…" suffix on the Role /
  StablePrefix layer) — the [ADR-0070](../adr/0070-model-visible-affordance-gate.md)
  gate, asserted against the layer the instruction owns.
  - verify: `TestScheduleSharedCatalog_Scenario3_SystemPromptCarriesScheduleNote`
- AC3.3: The shared engine's `OriginBinder` + `DeliveryQueue` (wired at
  `build.go:2529`/`:2535`) are live once the manager is bound, so a schedule
  created from a shared-engine session stamps its `OriginSessionID` and its
  fire's result is delivered back into that chat
  ([ADR-0075](../adr/0075-fire-result-delivery.md)).
  - verify: `TestScheduleSharedCatalog_Scenario3_OriginAndDeliveryWired`
- AC3.4: A default-profile session persisted, then reloaded via
  `needsRehydration` (the restart path), still resolves the `Schedule` tool —
  the rehydration no longer lands it on a schedule-less shared engine.
  - verify: `TestScheduleSharedCatalog_Scenario3_RehydratedSessionKeepsTool`
