---
id: 02-origin-capture-binding
title: Origin capture — per-run session binding into the ScheduleManager wrapper
blocked_by: [01-spec-field-and-validation]
status: done
branch: "plan-fire-result-delivery/02-origin-capture-binding"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/fire-result-delivery
---

# Task brief

The capture seam is NEW plumbing. A catalog tool's `Execute` receives only
`(ctx, call, ws)` — no session id — and the session id does NOT exist at
catalog-assembly time: the per-session factory builds the catalog BEFORE
`session.New` mints the id, and the shared engine predates every session. So the
binding is **per-run**, not at assembly.

**The corrected mechanism (resolved after a first dispatch found the assembly-time
binding impossible):** a `SessionOriginScheduleManager` wrapper (new file
`engine/agent/sessionorigin.go`) wraps the real `port.ScheduleManager`. It holds
the current origin session id in a `sync/atomic.Pointer[session.SessionID]`. Its
`CreateSchedule` stamps `spec.OriginSessionID = <current id>` before delegating
to the wrapped manager's create-seam. The engine sets the id at the start of each
run via a new OPTIONAL `Deps` binder — the wrapper implements a narrow
`BindSessionOrigin(session.SessionID)` that the engine calls in `startRun`
(`engine/agent/loop.go`), exactly where the session is already available. The
wrapper is registered in the catalog in place of the raw manager
(`internal/app/catalog.go` `registerScheduleTool`).

**Why this is race-free:** the Schedule tool reports `ReadOnly()==false`, so
dispatch is mutate-serial — only one Schedule create runs at a time — and the
engine drives one session's run to a boundary before servicing another, so the
atomic pointer always names the session whose run is executing the tool. It works
identically for the shared engine (id set per-run) and a per-session engine (id
constant). It is NOT a ctx-value and NOT a `parentCaps` widening.

The model CANNOT supply or influence the id: the tool's JSON schema
(`engine/agent/scheduletool.go` `scheduleSchema`) must expose NO `origin`
argument, and the create path takes the id ONLY from the wrapper's bound pointer
(a model-supplied `origin` arg, if it appears in the raw JSON, is ignored — the
bound id wins). A nil/unbound pointer (a create before any run binds, which the
loop's structure prevents) degrades to an empty OriginSessionID, never a wrong id.

This touches the engine's exported surface (new wrapper + `Deps` field) — run
`task api:update`, commit `engine/api/*.txt`, add an `engine/CHANGELOG.md` note
(Added = minor) per ADR 0037.

## Acceptance criteria

- AC1.1: A schedule created via the in-chat `Schedule create` verb persists the
  calling session's id in `Spec.OriginSessionID`, bound via the per-run wrapper
  (not a ctx-value, not a model-supplied arg).
  - verify: `TestFireDelivery_Scenario1_CreateCapturesOriginSession`
- AC1.4: The tool schema exposes NO `origin` argument and the model cannot
  influence the captured id — delivery is always to the creating session.
  - verify: `TestFireDelivery_Scenario1_OriginNotModelForgeable`
- AC1.5: The `OriginSessionID` value never appears in any model-visible surface.
  An engine is built, a schedule is created via the tool, and the id is asserted
  absent from the captured `LLMRequest.System` and the `ToolResult` text (an
  executable assertion, per ADR 0070 — not a grep over a subset of render paths).
  - verify: `TestFireDelivery_Scenario1_OriginIDNotModelVisible`
