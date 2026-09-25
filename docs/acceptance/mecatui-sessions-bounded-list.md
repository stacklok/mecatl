# Mecatui sessions inventory bounded list — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — the existing selectable session inventory adopts the established package-private bounded-list contract, changing its ephemeral cursor/physical-row geometry without changing a public API, persistence boundary, protocol, or system architecture.
**Decision record:** None — this is a focused application of the landed Mecatui bounded-control contract; its surface-local geometry and navigation belong in this plan rather than a durable architecture record.
**Phase:** bounded-list adoption, sessions inventory slice
**Status:** landed, 2026-09-25. The implementation candidate passed its acceptance proofs, aggregate gates, and panel review; this transition becomes authoritative when its implementation PR merges.
**Delivery:** Split. Session filtering, incremental pagination, capability-authorized actions, and compact geometry need contract review before implementation changes their shared state path.
**Expected tasks:** 1
**Issue:** [stacklok/mecatl#1895](https://github.com/stacklok/mecatl/issues/1895).
**Plan PR:** [#1906](https://github.com/stacklok/mecatl/pull/1906)

This slice migrates only the selectable `/sessions` inventory to the existing pointer-owned `bounded.List`. The current panel owns a numeric cursor and applies `scrollWindow` before width-dependent row rendering in [`cmd/mecatui/ui/sessions_surface.go`](../../cmd/mecatui/ui/sessions_surface.go). The migration makes session ID the list identity and lets the existing control account for each row's actual wrapped physical height.

Tabs, search, incremental server pagination, action capability checks, confirmation/forms, transcript browsing, and panel chrome remain session-surface behavior. The work does not alter which sessions are visible or actionable; it makes the inventory's physical viewport bounded and stable across the surface's existing refreshes.

## Human decisions

- [x] Identity and refresh — Decision: every inventory `ListItem` uses the opaque session ID as its stable ID. Filtering, page append/replacement, rename refresh, and resize retain both the selected session and the top visible `{session ID, physical line}` anchor when still present. If the selection disappears, adopt the bounded control's clamped replacement without later snap-back; switching tabs retains the current behavior of selecting the first row of the newly selected tab.
- [x] Geometry and presentation — Decision: render the existing row fields through `bounded.Wrap`, with a two-cell marker gutter: the standard selection cell plus the existing state badge. At normal geometry, derive the list body from the actual offered panel height after tab chrome, visible pagination status, and the action footer, cap it at twelve physical rows including any above/below indicator chrome, and render every complete panel line within the offered width. If that fixed chrome plus one body row cannot fit, render a one-line, width-clipped compact fallback with only the close action; it constructs no list and claims no session-navigation or action keys. Nonpositive geometry renders no panel. The existing tab bar, pagination status, action footer, and empty/loading/error/maintenance states stay surface-owned.
- [x] Navigation — Decision: preserve Up/Down through their existing bindings and use the existing `ScrollTop`/`ScrollBottom` bindings for first/last selection; their configured chords, rather than literal `home`/`end` text, are authoritative. While a normal session inventory is visible, the existing remappable `ScrollU`/`ScrollD` actions page the bounded physical window using its item-aware movement; they first expose an oversized selected row's remaining physical segments before changing selection. Wheel remains consumed without changing the inventory cursor or viewport, pending the separate owner-aware smooth-wheel work in #1790. No click, hover, pointer activation, or horizontal panning is added.
- [x] Actions and documentation — Decision: Enter and the existing `y`, `v`, `f`, `r`, and `d` actions continue to target the bounded list's selected session and retain all existing capability, current-session, confirmation, and server revalidation behavior. Update the existing `/sessions` reference in [`docs/tui.md`](../tui.md) with its physical paging keys; no new public page or configuration documentation is needed.

## Interface contract

- **gRPC / protobuf:** None — inventory data continues to use the existing session-list and transcript client calls without message, RPC, or compatibility changes.
- **Exported Go APIs / interfaces:** None — the work consumes the existing import-restricted `cmd/mecatui/ui/internal/bounded.List` and `presentListRow`; it adds no exported Go symbol or interface.
- **Tool schemas:** None — no model-facing tool name, input, output, or instruction changes.
- **CLI / config:** None — no flags, settings, key-binding names, or defaults change. While a normal session inventory owns the surface, existing configured `Up`, `Down`, `ScrollU`, `ScrollD`, `ScrollTop`, and `ScrollBottom` bindings drive the list; the dispatcher must honor rebindings rather than inspect literal key text. The compact fallback owns only Close.
- **Events / persistence:** None — list items, stable IDs, selection, anchors, physical offset, and geometry remain ephemeral client state; session records, pagination cursors, and mutation events retain their existing formats and owners.
- **Security / authority:** None — the list only projects rows and capability metadata already admitted by the existing session inventory. It does not expand session discovery, authorization, action eligibility, transcript access, clipboard access, filesystem access, or secret handling.
- **Compatibility / migration:** Replace the inventory's numeric cursor and fixed logical `scrollWindow` projection with one pointer-owned `bounded.List`. Preserve tab membership, search fields, deterministic ID-deduplicated pagination, visible pagination/error status, session-row text and terminal sanitization, current/draft/legacy/member markers, and all action routing. Transcript browsing remains on its existing Bubble Tea viewport and the active conversation viewport remains out of scope.

## In scope — 1 scenario, in implementation order

### Scenario 1 — sessions remain selectable while their variable-height rows fit a bounded physical viewport

When `/sessions` displays a nonempty normal inventory tab, the surface supplies stable session-ID items and actual panel geometry to `bounded.List`. The control owns only wrapped physical layout, cursor identity, semantic anchors, indicators, and item-aware movement. The session surface continues to own row construction, state/status semantics, tab and search filtering, page loading, footer text, capability checks, forms, transcript activation, and key routing. This follows the pointer-owned control, dynamic-gutter, refresh, and logical-overflow decisions in [the landed bounded-control plan](mecatui-bounded-scroll-selection.md#human-decisions), the existing session-continuity contract in [`docs/tui.md`](../tui.md), and the UI ownership rules in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC1.1: At tiny, short, narrow, and normal offered panel geometry—including a visible pagination-status row—the complete normal `/sessions` panel has no ANSI-stripped line wider than its offered display-cell width and no rendered height greater than its offered height. Its list body renders no more than twelve physical rows including any above/below indicator chrome; every row reserves the standard two-cell selection/state gutter through `presentListRow`, retains the existing state badge and terminal-safe session fields, and keeps selected/unselected styling distinct. If tab chrome, visible pagination status, action footer, and one list row cannot fit, the surface instead renders its one-line width-clipped compact fallback, constructs no list, and exposes only Close; nonpositive geometry renders no panel.
  - verify: `TestMecatuiSessionsBoundedList_Scenario1_GeometryPresentationAndIndicators`
- AC1.2: Configured Up/Down bindings move exactly one logical session; configured `ScrollTop`/`ScrollBottom` bindings select the first/last session; and configured `ScrollU`/`ScrollD` bindings traverse the bounded physical window with the established item-aware paging rules. The proof uses non-default bindings for all six actions. An oversized selected session row exposes all of its contiguous wrapped segments before paging changes to another session. Wheel remains consumed without moving the list or changing selection.
  - verify: `TestMecatuiSessionsBoundedList_Scenario1_NavigationPagingAndWheelOwnership`
- AC1.3: Search filtering, page append/replacement, rename refresh, row deletion, and resize preserve the selected session ID and top visible semantic anchor when present. When the selected session no longer exists, the list adopts its clamped replacement and does not resurrect the removed selection after a later refresh. Tab switching retains its existing first-row selection behavior.
  - verify: `TestMecatuiSessionsBoundedList_Scenario1_StableIdentityAcrossSurfaceRefreshes`
- AC1.4: Enter and `y`, `v`, `f`, `r`, and `d` operate on the bounded list's selected session and preserve existing capability-denied notices, active-chat deletion refusal, confirmation/forms, pagination cancellation/retry, and transcript/continuation behavior. Loading, error, empty, Maintenance, and compact-fallback states do not construct or route through an inventory list; the compact fallback permits only Close.
  - verify: `TestMecatuiSessionsBoundedList_Scenario1_PreservesActionsAndNonInventoryStates`
- AC1.5: The existing `/sessions` reference documents physical-page navigation through the remappable `ScrollU` and `ScrollD` actions without duplicating implementation-specific gutter, wrapping, overflow, or anchor details.
  - verify: inspection — verify [`docs/tui.md`](../tui.md) remains the sole owning user reference and pass `task docs`.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Stored-session transcript inspector | #1903 | The transcript has independent replay, tail, and footer behavior; this slice changes only the inventory list. |
| Active conversation viewport, text selection, live streaming, and tail following | Existing conversation ownership work | They are not generic inventory browsing and remain explicitly excluded by #1742. |
| Smooth-wheel burst coalescing or inventory wheel scrolling | #1790 | Preserve the current consumed-but-static wheel behavior rather than coupling list adoption to input-performance policy. |
| Session filtering rules, server pagination protocol, capability computation, actions, forms, and maintenance UI | Existing sessions surface contract | The bounded control owns neither domain data nor activation. |
| Click, hover, drag, touch, or horizontal interaction | Later explicitly planned interaction work | This slice preserves keyboard selection and explicit action activation. |
| Other bounded-list or viewport candidates | #1896–#1903 | Each surface remains independently classified and reviewed. |

## Definition of done

1. Focused sessions/UI and bounded-control tests, `task lint`, `task test`, and `task test:race` pass.
2. `task docs` passes after updating the sole owning `/sessions` reference.
3. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green for the runtime change.
5. The implementation PR links this approved Plan / Interface PR and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The exact `sessionsState` field layout, list-item builder, and rendering-helper names are implementation details. The `bounded.List` must be pointer-owned so Bubble Tea value-model copies preserve its state.
- The list must use `presentListRow`; no sessions-specific recreation of cursor, state-gutter, padding, or selected-style policy is permitted.
- Preserve the existing row text and terminal sanitization before supplying it to bounded layout. The bounded package remains theme-, Bubble Tea-, client-, and session-domain-free.
- Footer/chrome height must be measured from the rendered panel rather than assumed from `sessionsVisibleRows`; a second local physical-window calculation would reintroduce competing geometry authority.
