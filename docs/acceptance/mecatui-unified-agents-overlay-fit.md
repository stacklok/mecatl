# Mecatui unified Agents overlay fit — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this is a substantive, client-local interaction and layout correction with no durable architecture or public-contract decision.
**Decision record:** None — the change aligns the F6 Agents overlay’s selectable rows with the established Sessions picker treatment while making its already-promised viewport bounds demonstrable.
**Phase:** mecatui rendering correctness
**Status:** in-progress, 2026-09-13. Approved contract implementation is underway from `7a90f184297b653c5db255160f2272426c633ba8`.
**Delivery:** Split. The cross-tab interaction and viewport contract needs human interface review before implementation.
**Expected tasks:** 2
**Issue:** [stacklok/mecatl#1446](https://github.com/stacklok/mecatl/issues/1446).
**Plan PR:** [#1447](https://github.com/stacklok/mecatl/pull/1447)
**Amendment:** 2026-09-13 — the operator explicitly waived the Split amendment process for #1446 and authorized this implementation-PR amendment: compact mode keys on terminal height, not the reduced conversation viewport.
**Approved baseline:** `7a90f184297b653c5db255160f2272426c633ba8`

The F6 unified Agents overlay must make selection visible without per-row bordered-button
chrome and must remain usable within its offered conversation viewport. The implementation
will use the Sessions picker’s unbordered `accent` selection treatment and cursor-following
windowing principle, while retaining the literal `▶ ` marker selected for alignment. It does
not copy the Sessions picker’s fixed twelve-row, height-ignorant policy.

The height contract covers every F6 view: Subagents and child focus; Parallel groups and
branch focus; Teams roster and member focus; Team tasks and findings; and empty states. At a
known terminal height from 1 through 23, it renders an unframed, width-truncated compact line
containing `▶`, the active tab label, and `esc close` for a roster; a focus view instead
contains `▶`, the focused-item label, and `esc back`. Compact mode suspends normal-content
navigation. At a known terminal height of at least 24, normal mode applies; every F6 view must
fit its offered conversation viewport and overflowed content is keyboard-reachable with the
remappable `Up`, `Down`, `ScrollU`, `ScrollD`, `JumpTop`, and `JumpEnd` actions. Footers render
their live markings and accurate visible ranges. This remains a pure mecatui client rendering
change, as mecatui is documented as an event-rendering client in [`docs/tui.md`](../tui.md).

## Human decisions

- [x] Selected-row treatment — Decision: F6 selectable rows use the Sessions picker’s literal `▶ ` glyph with unbordered `accent` styling; the Sessions picker itself is unchanged.
- [x] Parallel winner semantics — Decision: a selected winning Parallel branch displays both `▶` and `★`; active-tab `▸` semantics are unchanged.
- [x] F6 scope — Decision: apply the contract to every Subagents, Parallel, and Teams subview; do not migrate unrelated pickers in this change.
- [x] Overflow traversal controls — Decision: every normal-height F6 subview reuses the existing remappable `Up`, `Down`, `ScrollU`, `ScrollD`, `JumpTop`, and `JumpEnd` actions. Roster and Parallel-branch views move the selection; Subagent/Team traces and Team tasks/findings scroll rendered lines. `ScrollU`/`ScrollD` move by the current physical-line window, endpoints clamp, selection remains visible, and footers show live markings plus the visible range.
- [x] Compact-mode contract — Decision: at known terminal heights 1–23, an unframed width-truncated roster line contains `▶`, the active tab label, and `esc close`; a focus line contains `▶`, the focused-item label, and `esc back`. Compact mode suspends normal-content navigation. Terminal heights of at least 24 use normal mode, bounded by the reduced conversation viewport.

## Interface contract

- **gRPC / protobuf:** None — existing delegation and team event payloads are only re-rendered by the client.
- **Exported Go APIs / interfaces:** None — the work is private to `cmd/mecatui/ui` renderer and view-state helpers.
- **Tool schemas:** None — no model-facing tool changes.
- **CLI / config:** None — no flag, key binding name, setting, or default changes; existing overlay controls retain their bindings.
- **Events / persistence:** None — no event, snapshot, or persisted client-state schema changes.
- **Security / authority:** None — preserve existing terminal sanitization, redacted delegation content, and client-only observation boundaries; scrolling exposes only content already admitted to the overlay.
- **Compatibility / migration:** Existing F6 bindings retain their meanings. Visible selection changes from `› ` plus a bordered button to `▶ ` plus unbordered accent styling; selected Parallel winners render both `▶` and `★`. Update the canonical mecatui keyboard/UI guidance in `user-docs/mecatui/keybindings.md` and the detailed TUI reference in `docs/tui.md`.

## In scope — 2 scenarios, in implementation order

### Scenario 1 — consistent accessible selection across the F6 overlay

A user navigates selectable rows in the Subagents, Parallel, and Teams tabs. The Sessions
picker already separates selection from the row’s domain state with an unbordered accent
style; the Agents overlay must use the same treatment without conflating the active-tab
marker or Parallel winner state. This preserves the focused overlay’s client-only
observability role described in [`docs/tui.md`](../tui.md) and the targeted, minimal-change
discipline in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC1.1: The selected Subagent, Parallel-group, Parallel-branch, and Team-member roster row begins with `▶ ` and uses unbordered `accent` styling; an unselected row keeps a two-cell prefix and muted styling.
  - verify: `TestMecatuiAgentsOverlayFit_Scenario1_SelectedRowsUseSessionsTreatment`
- AC1.2: Selection styling adds no border, padding, or background-derived physical rows when the selected row wraps.
  - verify: `TestMecatuiAgentsOverlayFit_Scenario1_SelectedWrappedRowHasNoButtonChrome`
- AC1.3: The active-tab `▸` marker remains distinct, and a selected winning Parallel branch renders both `▶` and `★` in ANSI-stripped output.
  - verify: `TestMecatuiAgentsOverlayFit_Scenario1_SelectedWinnerRetainsBothMarkers`
- AC1.4: The Sessions picker remains behaviorally and visually unchanged in this change.
  - verify: inspection — the implementation changes no Sessions picker renderer or tests because this contract intentionally limits migration to the F6 overlay.

### Scenario 2 — viewport-bounded and reachable Agents-overlay content

A user opens any F6 Agents-overlay tab at ordinary, narrow, or short terminal sizes. The
renderer must budget physical wrapped lines—including card frame, tab strip, fixed metadata,
optional failure/background detail, overflow indicators, and footer—before rendering the
final card. Cursor navigation and windowing must use that same current render budget, as the
shared cursor-following window pattern documents in [`cmd/mecatui/ui/window.go`](../../cmd/mecatui/ui/window.go). The implementation must retain the client-only layering and targeted rendering discipline in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC2.1: At every known terminal height from 24 through 80, each fully framed normal F6 subview fits its offered conversation viewport after ANSI-aware wrapping; the fit test exercises 32-, 80-, and 120-column widths, and no result relies on `lipgloss.Place` clipping.
  - verify: `TestMecatuiAgentsOverlayFit_Scenario2_AllSubviewsFitViewport`
- AC2.2: At each known terminal height from 1 through 23, the overlay uses the deterministic compact fallback instead of an oversized normal card; it renders no rows outside its offered conversation viewport and retains the selected compact-state and `esc` behavior.
  - verify: `TestMecatuiAgentsOverlayFit_Scenario2_CompactFallbackFitsShortViewport`
- AC2.3: Each selectable roster and focused Parallel branch view windows by rendered physical-line capacity, charging frame, tab strip, metadata, wrapped rows, tails, and footer; Page Up/Page Down uses that same capacity and leaves the selected row wholly visible.
  - verify: `TestMecatuiAgentsOverlayFit_Scenario2_RosterPagingMatchesRenderedWindow`
- AC2.4: Overflowed Subagent and Team traces, Parallel branches, Team tasks, and Team findings are traversable with the selected remappable controls; their footer uses the live markings and reports an accurate visible range.
  - verify: `TestMecatuiAgentsOverlayFit_Scenario2_OverflowContentReachable`
- AC2.5: Wrapped failure causes, background notices, long metadata, and unbreakable row text are charged to the same height budget and do not hide the required footer or selected row.
  - verify: `TestMecatuiAgentsOverlayFit_Scenario2_WrappedDynamicContentFitsViewport`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Migrating the Sessions picker, Models picker, MCP picker, or other overlays to the `▶ ` marker | Future cross-picker visual-alignment work | The Sessions picker is the reference treatment only; unrelated migration would broaden this client correction. |
| Changing delegation/team event payloads, privacy boundaries, or server behavior | Existing delegation observability contracts | The UI already receives the redacted metadata it may display. |
| Redesigning F6 tab order, child cancellation semantics, or permission-modal behavior | Existing mecatui interaction contracts | This work changes selection presentation and overflow access only. |

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, `task site:build`, and `task api:check` gates pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The exact private helper shape is intentionally deferred: reuse existing `scrollWindow` where uniform rendered rows permit it; introduce only the smallest physical-line budgeting helper necessary for the tabbed overlay. Do not create a general modal framework.
- The planned scrolling controls must use live key-map markings and must not leak key events into the prompt while the overlay is open.
