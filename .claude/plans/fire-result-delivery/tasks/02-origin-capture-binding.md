---
id: 02-origin-capture-binding
title: Origin capture — per-session binding into the ScheduleManager closure
blocked_by: [01-spec-field-and-validation]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/fire-result-delivery
---

# Task brief

The capture seam is NEW plumbing. A catalog tool's `Execute` receives only
`(ctx, call, ws)` — no session id — so the origin id is bound **per-session**
into the Schedule tool's `port.ScheduleManager` closure by COMPOSITION at
catalog-assembly time (the per-session engine factory / `sessionEngineFactory`
already assembles a catalog per session). It is NOT a ctx-value and NOT a
`parentCaps` widening (layering / fragility).

Concretely: the composition seam that builds the `port.ScheduleManager` the
Schedule tool drives (`internal/app/catalog.go` `registerScheduleTool`, backed by
the Service's schedule surface) gains a per-session variant that closes over the
session id, so the tool's `create` verb stamps that id onto
`spec.OriginSessionID` before the create-seam. The model CANNOT supply or
influence it: the tool's JSON schema exposes NO `origin` argument
(`engine/agent/scheduletool.go` `scheduleSchema`), and the create path takes the
id only from the bound closure. The shared-engine path (no per-session factory)
must still capture the creating session's id correctly — the manager closure is
bound per session regardless of which engine serves it.

## Acceptance criteria

- AC1.1: A schedule created via the in-chat `Schedule create` verb persists the
  calling session's id in `Spec.OriginSessionID`, bound via the composition
  closure (not a ctx-value, not a model-supplied arg).
  - verify: `TestFireDelivery_Scenario1_CreateCapturesOriginSession`
- AC1.4: The tool schema exposes NO `origin` argument and the model cannot
  influence the captured id — delivery is always to the creating session.
  - verify: `TestFireDelivery_Scenario1_OriginNotModelForgeable`
- AC1.5: The `OriginSessionID` value never appears in any model-visible surface.
  An engine is built, a schedule is created via the tool, and the id is asserted
  absent from the captured `LLMRequest.System` and the `ToolResult` text (an
  executable assertion, per ADR 0070 — not a grep over a subset of render paths).
  - verify: `TestFireDelivery_Scenario1_OriginIDNotModelVisible`
