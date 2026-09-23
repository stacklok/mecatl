# Mecatui bounded scroll and cursor control — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this establishes a substantive package-private TUI interaction contract across existing client surfaces without changing a public API, persistence boundary, protocol, or system architecture.
**Decision record:** None — the control remains confined to `cmd/mecatui/ui`; its local ownership and interaction choices belong in this plan rather than a durable architecture record.
**Phase:** bounded scroll-cursor convergence, slice 1
**Status:** proposed, 2026-09-15. Initial slice of the incremental migration tracked by #1589.
**Delivery:** Split. Shared cursor, layout, style, and pointer semantics need human interface review before implementation changes multiple surfaces.
**Expected tasks:** 2
**Issue:** [stacklok/mecatl#1589](https://github.com/stacklok/mecatl/issues/1589).
**Plan PR:** [#1593](https://github.com/stacklok/mecatl/pull/1593)

Mecatui will establish package-private bounded browsing and logical-item cursor behavior by consolidating or extending the existing helpers in [`cmd/mecatui/ui/window.go`](../../cmd/mecatui/ui/window.go) and [`cmd/mecatui/ui/agents_overlay.go`](../../cmd/mecatui/ui/agents_overlay.go). The behavior is proved in the unified Agents overlay—whose rosters and details exercise both modes—and the `/models` picker. Filtering, actions, domain state, confirmation, and data loading remain surface-owned; private helper names and type layout are not acceptance surfaces.

This is an incremental targeted correction, not the future formal migration of every TUI surface. It extends the viewport and compact-mode compatibility contract in [the unified Agents overlay plan](mecatui-unified-agents-overlay-fit.md). The specialized conversation viewport remains unchanged under [ADR 0301](../adr/0301-logical-conversation-anchors.md), and the client-only package boundary remains the one documented in [`docs/architecture.md`](../architecture.md).

## Human decisions

- [x] Initial consumers — Decision: prove multiline cursor rows and browsing details across every unified-Agents subview plus fixed-height cursor rows in `/models`; defer Skills and every other surface to independently classified follow-ups under #1589.
- [x] Control boundary — Decision: establish one shared physical-line windowing behavior with optional logical-item cursor state by consolidating, extending, or deleting the existing helpers; do not add exported interfaces, a general widget framework, or a second independent windowing policy.
- [x] Width and oversized-item behavior — Decision: the control owns the hard display-cell width bound after applying the caller-selected wrap-or-clip policy. A selected item taller than the available body remains selected while Page Up/Page Down traverses bounded contiguous segments of that item; every segment retains the cursor marker and exposes accurate overflow before advancing to another item.
- [x] Cursor presentation — Decision: cursor rows use the literal `▶ ` marker with unbordered `accent` styling and an equal-width unselected prefix; text selection, control focus, current `●`, default `★`, active-tab `▸`, and Parallel-winner state retain distinct meanings and styles.
- [x] Paging — Decision: Page Down starts at the first physical line after the current window and selects the first logical item visible there; Page Up applies the symmetric preceding-window rule. An oversized selected item consumes successive pages before the cursor advances. Up/Down always moves one logical item and resets any within-item page position.
- [x] Pointer ownership — Decision: wheel input moves cursor mode by one logical item and browsing mode by one physical line. Models proves primary-click cursor movement without activation through the existing `surface` hit lifecycle. Agents receives a narrow root-level wheel branch before conversation scrolling; it gains no click targets or second hit registry. Compact and `vp short` Agents fallbacks consume wheel input without changing hidden state. Enter remains activation; hover styling is deferred.

## Interface contract

- **gRPC / protobuf:** None — all affected state and interactions remain inside the proto-free mecatui renderer.
- **Exported Go APIs / interfaces:** None — window and cursor behavior remains package-private under `cmd/mecatui/ui`; the conversation-view controller and public engine surface are unchanged.
- **Tool schemas:** None — no model-facing tool or argument changes.
- **CLI / config:** None — no flags, settings, key-binding names, or defaults change; existing remappable navigation actions remain authoritative.
- **Events / persistence:** None — cursor, within-item page position, browsing offset, geometry, and pointer-hit state remain ephemeral client state.
- **Security / authority:** None — the control renders only content already admitted to its owning surface and preserves frame-scoped opaque hit dispatch; it introduces no new content, trust, permission, or ownership path.
- **Compatibility / migration:** Every unified-Agents roster/detail view and `/models` adopts the shared behavior. Agents and Models cursor rows converge on `▶ ` plus unbordered `accent`; wheel input is consumed by the visible owner; Models gains click-to-cursor without click-to-activate. Agents compact-mode navigation remains suspended. Agents clicks, conversation scrolling, and every unlisted surface remain unchanged.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — bounded multiline browsing and cursor movement

A caller supplies explicit content width and physical-line height together with logical items or rendered browsing lines. The control accounts for ANSI-aware display width and physical height, keeps cursor identity separate from physical offsets, and exposes all oversized selected-item content without exceeding its bounds. It generalizes rather than duplicates the physical budget already established by [the Agents overlay plan](mecatui-unified-agents-overlay-fit.md), following the minimum-change discipline in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC1.1: At positive width and height, every wrap or clip policy produces no more than the supplied physical-line height and no ANSI-stripped line wider than the supplied display-cell width.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario1_RespectsWidthAndHeight`
- AC1.2: Up/Down moves one logical item; Page Down selects the first item beginning after the current physical window and Page Up applies the symmetric preceding-window rule; Top/End clamps to the first/last item.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario1_MultilinePagingTargets`
- AC1.3: A cursor item taller than the available body is shown in bounded contiguous segments, retains its cursor marker, exposes accurate above/below overflow, and is completely reachable by paging before the cursor advances; ANSI style does not leak across clipped segment boundaries.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario1_OversizedCursorItemReachable`
- AC1.4: Browsing line/page movement and cursor windows clamp after content or geometry changes without a reachable blank page; nonpositive width or height yields an empty body and no panic; no horizontal offset or panning action exists.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario1_ClampsContentAndDegenerateBounds`

### Scenario 2 — representative surfaces honor real geometry and cursor semantics

Agents and Models use the same physical accounting for their complete rendered surface, rather than passing nominal row counts that can exceed narrow or short geometry. Domain-status markers remain independent from the cursor. The complete outputs, not only the helper, are tested against the dimensions each surface is actually offered while preserving the proto-free client boundary in [`docs/architecture.md`](../architecture.md).

**Acceptance:**
- AC2.1: At tiny, narrow, normal, and wide dimensions, every normal Agents subview either fits its complete offered viewport or takes the existing compact/`vp short` fallback; compact mode still exposes only its documented Escape behavior.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario2_AllAgentsSubviewsFitOfferedGeometry`
- AC2.2: Subagent, Parallel-group, focused-Parallel-branch, and Team rosters keep cursor rows visible under asymmetric multiline heights; Subagent/Team traces and Team tasks/findings use bounded browsing offsets with accurate overflow.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario2_AgentsModesUseSharedAccounting`
- AC2.3: The complete Models surface derives its row body from the actual offered width and height, including fixed chrome and narrow wrapped/clipped rows; it has no minimum-row rule that can force output beyond the offered geometry.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario2_ModelsFitsOfferedGeometry`
- AC2.4: Selected Agents and Model rows use `▶ ` plus unbordered `accent` with an equal-width unselected prefix, while Model current `●`/default `★` and Agents active-tab `▸`/Parallel-winner markers remain independently visible.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario2_CursorAndStatusStylesStayDistinct`
- AC2.5: Existing focused regressions for palette, mention, Sessions, MCP, Effort, conversation scrolling, and text selection remain green; shared helper changes do not silently migrate their behavior.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario2_UnlistedSharedConsumersUnchanged`

### Scenario 3 — visible owners consume consistent mouse input

In alternate-screen mouse mode, wheel and primary-click input target the visible owner rather than hidden conversation content. Models uses the existing frame-scoped hit lifecycle documented in [the surface contract](../design/surface-migration-plan.md#shipped-approval-render-frame-hit-dispatch); Agents receives only the explicit root wheel branch needed before its future formal `surface` migration.

**Acceptance:**
- AC3.1: Wheel over Subagent, Parallel-group, focused-Parallel-branch, or Team rosters moves one logical item; wheel over Subagent/Team traces or Team tasks/findings moves one physical browsing line; every path clamps at both ends.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario3_AgentsWheelSubviewMatrix`
- AC3.2: Compact or `vp short` Agents views consume wheel without changing hidden cursor/detail offsets, and wheel while Agents or Models owns the body never changes the hidden conversation viewport—even when the owning control is at its boundary.
  - verify: `TestMecatuiBoundedScrollCursor_Scenario3_WheelNeverLeaksOrNavigatesFallback`
- AC3.3: Wheel over Models moves its cursor by one logical item. A primary click on the marker or text-bearing cells of any visible physical line of a Model row moves the logical cursor without switching models; clicks on chrome, overflow indicators, or trailing blank space miss; Enter retains activation.
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
