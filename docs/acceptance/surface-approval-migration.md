# Surface approval migration — acceptance plan

**Phase:** issue #555 Phase 2 — final approval-surface migration
**Status:** landed, 2026-08-21. All implementation tasks and aggregate gates completed on the stacked accumulator.
**Issue:** [stacklok/mecatl#555](https://github.com/stacklok/mecatl/issues/555).
**ADR:** [ADR-0069](../adr/0069-plan-approval-gate.md) — plan approval reuses permission asks and mecatui must present the operator-review surface without changing its approval semantics.
**Accumulator branch:** `acc/surface-approval-migration` (stacked on the `surface-models-migration` / PR #731 head).

The smallest set of work that moves the mecatui approval UI onto the established
`surface` contract while preserving approval, queueing, plan-review, args-review,
keyboard, wheel, mouse, stream, and continuation behavior. It also supplies the
first render-frame hit-dispatch seam: ephemeral hit IDs identify exactly the
regions drawn in one frame and stale deliveries fail closed. It does not generalize
mecatui into a multi-window manager or alter approval policy/product behavior.

## Why these scope cuts

- [ADR-0069](../adr/0069-plan-approval-gate.md) fixes the meaning and terminal ordering of plan approval. This UI refactor must not change the `PresentPlan` gate, verdicts, or post-terminal continuation.
- [`architecture.md` — mecatui](../architecture.md) keeps `cmd/mecatui/ui` a proto-free client that resolves an ask by sending `ResumeApproval` on the existing stream; the surface does not take ownership of transport.
- [`surface-migration-plan.md`](../design/surface-migration-plan.md) fixes the Phase-2 contract: one dynamic modal, surfaces size while parents place, and Model retains effects whose lifetime exceeds the surface.
- [`IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md) specifies that plan review is scrollable, args review remains type-specific, and the interactive continuation starts only after terminal `plan_approved`.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — A permission ask dynamically owns its approval surface

A first `PermissionAskMsg` creates a dynamic approval surface. That surface holds the
visible ask, FIFO successors, dedupe set, focus, and plan/args/mini-viewport view
state only while it is open. The Model retains the current stream, transcript notices,
run phase, spinner lifecycle, textarea focus, and terminal plan continuation. This
applies the lifetime rule in [`surface-migration-plan.md`](../design/surface-migration-plan.md)
without changing the TUI’s existing same-stream `ResumeApproval` transport described in
[`architecture.md`](../architecture.md).

**Acceptance:**
- AC1.1: The first permission ask installs one dynamic approval surface; a duplicate is ignored and later surfaced asks queue FIFO behind its visible head.
  - verify: `TestSurfaceApprovalMigration_Scenario1_DynamicSurfaceQueue`
- AC1.2: Resolving or retracting a visible ask keeps the same surface and `phaseAwaitingApproval` while a successor exists; resolving or retracting the final ask clears both render-frame caches, removes the surface, restores the interrupted idle/running phase, refocuses the textarea, and re-arms the spinner only for a running resume.
  - verify: `TestSurfaceApprovalMigration_Scenario1_QueueDrainRetractAndCloseLifecycle`
- AC1.3: Session reset and terminal-run cleanup close the dynamic approval surface and discard its queued asks and render caches, so no approval view state crosses a session/run boundary.
  - verify: `TestSurfaceApprovalMigration_Scenario1_RunAndSessionTeardown`
- AC1.4: `Model` has no permanent approval-state tombstone; the approval surface is dynamically constructed only at ask open, consistent with the surface migration contract.
  - verify: `TestApprovalSurfaceStateIsNotModelOwned`

---

### Scenario 2 — Existing approval semantics remain byte-for-byte and transport-safe

An operator can review ordinary permission asks, plan asks, and full args views through
the migrated surface. The surface emits semantic intents while Model performs the
existing transport, notice, phase, spinner, and post-terminal continuation effects. The
plan-review and continuation ordering remain those recorded in
[`IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md) and [ADR-0069](../adr/0069-plan-approval-gate.md).

**Acceptance:**
- AC2.1: Keyboard verdicts, focus movement, child-ask withholding of Allow Always, `/debug-ask` with a nil stream, exact ask-ID verdict sending, and the no-extra-reader rule retain current behavior.
  - verify: `TestSurfaceApprovalMigration_Scenario2_VerdictTransportAndChildPolicy`
- AC2.2: A plan ask remains a scrollable fill view with pinned verdict controls; its plan viewport rewraps when its ask, queued-count/model presentation, or geometry changes and preserves the reading offset on a no-op relayout.
  - verify: `TestSurfaceApprovalMigration_Scenario2_PlanReviewLayoutAndOffset`
- AC2.3: Non-plan/non-diff asks retain their centered mini-args view and full args review, including raw/pretty switching, preserved offset, and keyboard verdicts from the expanded view; diff asks retain their existing expansion path.
  - verify: `TestSurfaceApprovalMigration_Scenario2_ArgsAndDiffModes`
- AC2.4: A `plan_approved` continuation begins only after the terminal result, never immediately after a verdict send, preserving ADR-0069’s same-stream ordering rule.
  - verify: `TestSurfaceApprovalMigration_Scenario2_PlanContinuationOrdering`

---

### Scenario 3 — Mouse delivery targets only the rendered frame

Every clickable surface region receives a fresh opaque hit ID during rendering. A
Model-owned render collaborator retains only the current screen-adjusted frame for
parent hit testing; each surface retains only its current ID-to-local-behavior view
cache. A raw click becomes a typed local-coordinate message delivered through normal
surface message routing. This preserves the `surface` contract and the shared
render/region layout invariant in [`surface-migration-plan.md`](../design/surface-migration-plan.md).

**Acceptance:**
- AC3.1: A Model-owned opaque allocator is injected through surface dependencies; `ClickableRegion` carries the resulting hit ID rather than a globally meaningful approval action, and every real `View()` render replaces the parent screen-frame and owning-surface ID-to-local-behavior caches with fresh IDs for the current frame.
  - verify: `TestSurfaceApprovalMigration_Scenario3_RealViewPersistsFreshFrameHitCaches`
- AC3.2: The parent applies placement exactly once, hit-tests the current screen-space frame, translates the pointer to region-local coordinates, and delivers the typed hit message through ordinary surface routing without widening `surface`.
  - verify: `TestSurfaceApprovalMigration_Scenario3_HitCoordinatesAndMessageRouting`
- AC3.3: An unknown, stale, closed-surface, or superseded-frame hit ID causes no verdict, notice, transport send, focus change, or underlying transcript selection.
  - verify: `TestSurfaceApprovalMigration_Scenario3_StaleHitsFailClosed`
- AC3.4: Approval button hits in generic, plan, and full-args layouts resolve the same verdicts as their keyboard equivalents; clicks elsewhere in the approval view remain swallowed and no-mouse/inline modes remain inert.
  - verify: `TestSurfaceApprovalMigration_Scenario3_ApprovalMouseParity`
- AC3.5: Every open modal captures wheel input: ready plan/full-args views scroll their local view, generic cards scroll their mini-view regardless of pointer position, and other approval layouts consume. The conversation viewport receives wheel input only without a modal.
  - verify: `TestSurfaceApprovalMigration_Scenario3_ModalWheelCapture`

---

### Scenario 4 — The final Phase-2 migration leaves a durable boundary

The approval migration completes the declared Phase-2 modal order without enrolling
legacy overlays into this change. Structural and rendering tests prevent a later
regression from restoring approval-specific Model routing, global action identifiers, or
parallel geometry calculations. This follows the migration test-boundary rule in
[`surface-migration-plan.md`](../design/surface-migration-plan.md) and the client-layer
boundary in [`architecture.md`](../architecture.md).

**Acceptance:**
- AC4.1: Approval vocabulary, state, rendering, hit-ID cache, and input behavior remain confined to approval and shared hit-dispatch files; Model contains only registration, durable effects, and generic routing.
  - verify: `TestApprovalSurfaceStructuralBoundary`
- AC4.2: Approval rendering and golden frames are unchanged for all existing approval fixtures.
  - verify: demonstration — `task test:golden` proves the existing approval frames are byte-identical
- AC4.3: The living surface-migration design records the ephemeral-ID/frame-cache protocol, Model/surface ownership split, and the deliberately deferred multi-window manager, and retires its obsolete approval-surface exclusion.
  - verify: inspection — `task docs` validates citations and generated `llms.txt`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Approval policy, verdict semantics, plan-mode behavior, or user-visible redesign | focused follow-up issue | behavior-preserving migration only |
| Other legacy overlays (`/team`, agents, user model, reflections, dream, effort, worktrees, schedule) | after the current surface stack merges | this PR completes the declared Phase-2 stack, not all overlays |
| A multi-window manager, z-order/focus tree, or permanent surface/window IDs | Phase 3 | frame-local hit IDs are only a bridge to future window routing |
| Bubblezone dependency | none | reuse no external region/layout system; retain the repository’s render/region contract |
| Fixes for independently discovered behavior defects | separately filed issue | avoid hiding unrelated product work in this refactor |

## Sequencing recommendation

First introduce the shared render-frame hit-dispatch collaborator and its tests; its
fresh-ID/stale-drop rule is the safety boundary for approval clicks. Then move approval
state, rendering, keyboard/wheel/message handling, and local frame caches into the
dynamic surface while preserving Model-owned intents and lifecycle effects. Finally remove
the legacy Model approval field/routes, update the structural tests and living design
notes, and run the unchanged golden suite.

## Definition of done

1. `task lint` and `task test` pass.
2. `task test:golden` passes without approval golden changes.
3. `task docs` regenerates `llms.txt` and passes the strict documentation gate.
4. `task ac-trace-strict` resolves every acceptance proof after the plan is marked `landed`.
5. `go run ./cmd/mecademo` still prints a full offline session.
6. The resulting work is a new `gh stack` child of PR #731; #731 itself remains unchanged.

## Deferred decisions and known risks

- **Fresh IDs can expose frame-order drift as a dropped click.** This is intentional fail-closed behavior. Reusing IDs across frames is a future compatibility fallback only if a demonstrated Bubble Tea scheduling constraint requires it.
- **Frame caches are renderer state, not application state.** They must be replaced wholesale on render and cleared on surface close/run/session teardown; they must never influence layout or durable behavior.
- **The current parent is a one-modal interim window manager.** A future manager can reuse the hit-dispatch collaborator to choose a window from the rendered frame, then deliver its local hit message; this plan does not define that manager.

## Exit criteria

When every point under *Definition of done* holds on the accumulator branch, this plan is satisfied.
