# Mecatui bounded scroll and cursor control — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this establishes a substantive package-private TUI interaction contract across existing client surfaces without changing a public API, persistence boundary, protocol, or system architecture.
**Decision record:** None — the control remains confined to `cmd/mecatui/ui`; its local ownership and interaction choices belong in this plan rather than a durable architecture record.
**Phase:** bounded scroll-cursor convergence, slice 1
**Status:** landed in this implementation candidate, 2026-09-15. Authoritative when the implementation PR merges; the operator authorized opening without rebasing for repository-wide ac-trace fixes already present on current `origin/main`.
**Amendment:** 2026-09-15 — after the early package-private API checkpoint, the operator explicitly waived a separate amendment PR and authorized this implementation-branch amendment for stable item identity, independent viewport/cursor anchors, caller-owned selected styling, and tiny-width behavior.
**Delivery:** Split. Shared cursor, layout, style, and pointer semantics need human interface review before implementation changes multiple surfaces.
**Expected tasks:** 2
**Issue:** [stacklok/mecatl#1589](https://github.com/stacklok/mecatl/issues/1589).
**Plan PR:** [#1593](https://github.com/stacklok/mecatl/pull/1593)

Mecatui will establish package-private bounded browsing and logical-item cursor behavior by consolidating or extending the existing helpers in [`cmd/mecatui/ui/window.go`](../../cmd/mecatui/ui/window.go) and [`cmd/mecatui/ui/agents_overlay.go`](../../cmd/mecatui/ui/agents_overlay.go). The behavior is proved in the unified Agents overlay—whose rosters and details exercise both modes—and the `/models` picker. Filtering, actions, domain state, confirmation, and data loading remain surface-owned; private helper names and type layout are not acceptance surfaces.

This is an incremental targeted correction, not the future formal migration of every TUI surface. It extends the viewport and compact-mode compatibility contract in [the unified Agents overlay plan](mecatui-unified-agents-overlay-fit.md). The specialized conversation viewport remains unchanged under [ADR 0301](../adr/0301-logical-conversation-anchors.md), and the client-only package boundary remains the one documented in [`docs/architecture.md`](../architecture.md).

## Human decisions

- [x] Initial consumers — Decision: prove multiline cursor rows and browsing details across every unified-Agents subview plus fixed-height cursor rows in `/models`; defer Skills and every other surface to independently classified follow-ups under #1589.
- [x] Control boundary — Decision: use two concrete pointer-owned controls in `cmd/mecatui/ui/internal/bounded`: `bounded.Viewport` owns physical-line geometry and scroll position, projecting caller-provided lines as `ViewportView`; `bounded.List` composes it with `ListItem` identity and cursor state, projecting `ListRow` metadata as `ListView`. Its exported API is limited to projection values, item replacement, geometry, movement, selection, scrolling, and indicator-adjusted views; storage remains opaque. Do not add a Go interface, general widget framework, or second independent windowing policy.
- [x] Width and oversized-item behavior — Decision: the viewport owns the hard display-cell width bound after applying the caller-selected wrap-or-clip policy. Width below the fixed cursor gutter plus one content cell yields an empty body so the owner can use its compact fallback. An item taller than the body is exposed in bounded contiguous segments without ANSI/style leakage.
- [x] Cursor identity and refresh — Decision: cursor-mode items carry stable IDs. On refresh, retain the selected ID and clamp its within-item position; if it is absent, clamp the prior numeric index and adopt that fallback item's ID, with no later snap-back. Preserve the top visible `{item ID, physical line within item}` anchor when possible so insertion/removal above does not change what the user is reading; fall back to a clamped physical offset when the anchor disappears.
- [x] Cursor presentation — Decision: `bounded.List` returns metadata only. The UI-level `presentListRow` helper renders the configured per-list gutter: one selection cell (`▶` or blank), zero to two one-cell status markers, then padding; it owns selected/unselected style policy while callers retain themes, hit regions, headers/footers, overflow wording, and activation. `ListItem` stores at most two opaque status-cell values; bounded rejects multiline or non-single-display-cell markers before projecting `ListRow`, so status cannot break list geometry. Agents use the selection-only width; focused Parallel branches reserve one winner cell (`★`); Models reserve current `●` and global-default `★` cells. Text selection, control focus, and active-tab `▸` remain distinct.
- [x] Models sizing — Decision: bound Models list rows to the actual list-content geometry, but leave Models chrome and help text intact until the existing card/placement renderer measures it. The card retains its historical natural width within the offered terminal placement, so normal geometry shows complete help actions rather than a pre-truncated suffix.
- [x] Cursor and scroll movement — Decision: cursor identity/within-item position and viewport position are separate state. Up/Down moves one logical item and minimally scrolls to reveal it; Page Up/Page Down keeps the approved item-aware paging behavior, including oversized segments. Mouse wheel scrolls the viewport by one physical line without moving the cursor; clicking a Model row moves the cursor and minimally reveals it.
- [x] Bubbles reuse — Decision: reuse the installed Bubbles v2 and `x/ansi` patterns where they preserve the contract—separate cursor/viewport state, fixed gutter, caller-owned line styling, content-update clamping—but do not adopt `list.Model`, `table.Model`, or `viewport.Model` as the core because their fixed/indexed or private physical-line models cannot preserve variable-height stable item identity and hit metadata.
- [x] Pointer ownership — Decision: Models uses the existing `surface` hit lifecycle. Agents receives a narrow root-level wheel branch before conversation scrolling; it gains no click targets or second hit registry. Compact and `vp short` Agents fallbacks consume wheel without changing viewport or cursor state. Enter remains activation; hover styling is deferred.

## Interface contract

- **gRPC / protobuf:** None — all affected state and interactions remain inside the proto-free mecatui renderer.
- **Exported Go APIs / interfaces:** The import-restricted internal package `cmd/mecatui/ui/internal/bounded` exports concrete pointer-owned `Viewport` and `List` controls plus narrow `Policy`, `Move`, `ViewportView`, `ListItem`, `ListRow`, and `ListView` projection values. `Viewport.View` accepts caller-provided lines on every render; it retains only opaque geometry and offset state. It introduces no interface and no public engine or external Go API; callers retain themes, Bubble Tea messages, activation, and hit-region lifecycle.
- **Tool schemas:** None — no model-facing tool or argument changes.
- **CLI / config:** None — no flags, settings, key-binding names, or defaults change; existing remappable navigation actions remain authoritative.
- **Events / persistence:** None — stable item IDs, cursor index/within-item position, semantic top anchor, physical fallback offset, geometry, and pointer-hit state remain ephemeral client state.
- **Security / authority:** None — the control renders only content already admitted to its owning surface and preserves frame-scoped opaque hit dispatch; it introduces no new content, trust, permission, or ownership path.
- **Compatibility / migration:** Every unified-Agents roster/detail view and `/models` adopts the shared behavior. `bounded.List` is the sole persisted selection and viewport authority for Agents rosters and Parallel branches; parallel branch IDs are scoped by `ParentCallID` and branch index, while team member IDs are scoped by the immutable Team tool-call/block identity and member name (`TeamID` remains display metadata), so anchors cannot cross aggregates with coincident local IDs. `bounded.List` projects a configured 1–3-cell gutter and up to two validated status cells, while `presentListRow` is the single UI policy that renders selection, status, padding, and selected/unselected styles. Agents normal selectable lists use selection-only gutters; focused Parallel branches reserve a winner cell; Models reserve current/default cells. Models bounds only list rows to the available content geometry; its chrome remains available to the existing card renderer for natural-width measurement, preserving complete help actions at normal geometry. Agents and Models default cursor rows remain unbordered `accent`; wheel scrolls the visible viewport without moving its cursor; Models gains click-to-cursor without click-to-activate; item refresh preserves cursor and viewport anchors by stable ID. Agents compact-mode navigation remains suspended. Agents clicks, conversation scrolling, and every unlisted surface remain unchanged.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — bounded multiline browsing and cursor movement

A caller supplies explicit content width and physical-line height together with `ListItem` values or rendered browsing lines. `bounded.Viewport` owns physical-line layout and scroll position, returning a contentless `ViewportView` projection over caller-provided lines; `bounded.List` adds stable item identity and a cursor, returning `ListView` rows that can remain distinct from the viewport. Both account for ANSI-aware display width and expose oversized content without exceeding their bounds. They generalize rather than duplicate the physical budget already established by [the Agents overlay plan](mecatui-unified-agents-overlay-fit.md), following the minimum-change discipline in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC1.1: At width sufficient for the configured gutter plus one content cell and positive height, every wrap or clip policy produces no more than the supplied physical-line height and no ANSI-stripped line wider than the supplied display-cell width; a smaller width or nonpositive dimension yields an empty body without panic.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario1_RespectsWidthAndHeight`
- AC1.2: Up/Down moves the logical cursor one item and minimally reveals it; Page Down selects the first item beginning after the current physical window and Page Up applies the symmetric preceding-window rule; Top/End clamps to the first/last item.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario1_MultilinePagingTargets`
- AC1.3: An item taller than the body is shown in bounded contiguous segments and is completely reachable in both directions; only the first visible line of a selected segment carries the cursor-marker flag, continuation lines retain the fixed blank gutter, caller-selected styling remains independent, and ANSI style does not leak across boundaries.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario1_OversizedCursorItemReachable`
- AC1.4: Browsing movement, logical cursor state, and physical viewport position remain independently observable and clamp after content or geometry changes without a reachable blank page; no horizontal offset or panning action exists.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario1_ClampsContentAndDegenerateBounds`
- AC1.5: Replacing cursor-mode items preserves the selected stable ID and top visible `{item ID, item line}` anchor across insertion, removal, reordering, and text-height changes when those IDs remain; a missing selected ID falls back to the clamped prior numeric index and adopts the replacement ID without later snap-back, while a missing top ID falls back to a clamped physical offset.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario1_RefreshPreservesSemanticAnchors`

### Scenario 2 — representative surfaces honor real geometry and cursor semantics

Agents and Models use the same physical accounting for their complete rendered surface, rather than passing nominal row counts that can exceed narrow or short geometry. Domain-status markers remain independent from the cursor. The complete outputs, not only the helper, are tested against the dimensions each surface is actually offered while preserving the proto-free client boundary in [`docs/architecture.md`](../architecture.md).

**Acceptance:**
- AC2.1: At tiny, narrow, normal, and wide dimensions, every normal Agents subview either fits its complete offered viewport or takes the existing compact/`vp short` fallback; compact mode still exposes only its documented Escape behavior.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario2_AllAgentsSubviewsFitOfferedGeometry`
- AC2.2: Subagent, Parallel-group, focused-Parallel-branch, and Team rosters keep cursor rows visible under asymmetric multiline heights; Subagent/Team traces and Team tasks/findings use bounded browsing offsets with accurate overflow.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario2_AgentsModesUseSharedAccounting`
- AC2.3: The Models list body derives its rows from the actual offered width and height, including fixed chrome and narrow wrapped/clipped rows; Models chrome keeps its historical natural-width measurement at normal geometry while compact geometry remains hard-bounded. No minimum-row rule may force list output beyond the offered geometry.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario2_ModelsFitsOfferedGeometry`
- AC2.4: Selected Agents and Model rows default to `▶ ` plus unbordered `accent` with an equal-width unselected prefix, while the shared control allows each caller to supply a different selected-item style; Model current `●`/default `★` and Agents active-tab `▸`/Parallel-winner markers remain independently visible.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario2_CursorAndStatusStylesStayDistinct`
- AC2.5: Existing focused regressions for palette, mention, Sessions, MCP, Effort, conversation scrolling, and text selection remain green; shared helper changes do not silently migrate their behavior.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario2_UnlistedSharedConsumersUnchanged`

### Scenario 3 — visible owners consume consistent mouse input

In alternate-screen mouse mode, wheel and primary-click input target the visible owner rather than hidden conversation content. Models uses the existing frame-scoped hit lifecycle documented in [the surface contract](../design/surface-migration-plan.md#shipped-approval-render-frame-hit-dispatch); Agents receives only the explicit root wheel branch needed before its future formal `surface` migration.

**Acceptance:**
- AC3.1: Wheel over any normal Subagent, Parallel-group, focused-Parallel-branch, or Team roster/detail view scrolls its physical viewport by one line without moving its logical cursor; every path clamps at both ends, and subsequent keyboard cursor movement minimally reveals the selected item.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario3_AgentsWheelSubviewMatrix`
- AC3.2: Compact or `vp short` Agents views consume wheel without changing viewport or cursor state, and wheel while Agents or Models owns the body never changes the hidden conversation viewport—even when the owning viewport is at its boundary.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario3_WheelNeverLeaksOrNavigatesFallback`
- AC3.3: Wheel over Models scrolls its viewport by one physical line without moving its stable cursor. A primary click on the marker or text-bearing cells of any visible physical line of a Model row moves the cursor to that item and minimally reveals it without switching models; clicks on chrome, overflow indicators, or trailing blank space miss; Enter retains activation.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario3_ModelClickSelectsEnterActivates`
- AC3.4: Model hit regions are valid only for the render frame and geometry that produced them; old-frame, out-of-bounds, and closed-surface hits cannot change cursor or activate an action.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario3_StaleModelHitsIgnored`
- AC3.5: Canonical mecatui guidance documents visible-owner wheel behavior, Models click-to-cursor versus Enter activation, and the unchanged compact-Agents Escape-only contract.
  - verify: inspection — update the owning `user-docs/` mecatui model-selection/keybinding pages and detailed `docs/tui.md` interaction reference, then pass `task docs` and `task site:build`.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Command palette, mentions, Sessions, Worktrees, Schedule, MCP, Connect, Skills, User Model, Reflections, Dream, Help, Soul, agent inventory, approval, and other detail views | Follow-up slices under #1589 | Classify each batch independently; mechanical adoption may be Routine, while new interaction decisions remain Bounded. |
| Main conversation viewport, text-selection behavior, streaming-tail following, or virtualization | ADR 0301 and a future profiling-backed design | Do not fold transcript ownership or horizontal panning into this package-private control. |
| Formal migration of legacy overlays onto the `surface` lifecycle, including Agents click-to-cursor | Future TUI-system design | This slice adds only a narrow Agents wheel branch before conversation scrolling; it creates no second hit registry or broad modal framework. |
| Hover styling, pointer-driven activation, drag selection of rows, touch gestures, or horizontal panning | Later interaction work with a concrete use case | Mouse support is wheel plus Models click-to-cursor; activation remains explicit through Enter. |
| Filtering, server pagination, actions, confirmation, data loading, or domain state | Existing surface owners | The control owns only bounds, physical offsets, logical cursor visibility, rendering metadata, and optional surface-owned pointer targets. |

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, `task site:build`, and `task api:check` gates pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Private helper names and state layout remain implementation details. Existing `scrollWindow`, rendered-line windowing, and `physicalCursorWindow` behavior should be deleted or reused rather than retained as competing policies.
- ANSI-aware wrapping/clipping, physical paging, overflow, and Model hit geometry must derive from one rendered-row accounting path; a second geometry calculation risks pointer disagreement and style leakage.
- The targeted Agents wheel branch is compatibility debt, not the future modal architecture. It must remain visibly isolated so a later `surface` migration can delete it.
