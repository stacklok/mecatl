# Mecatui bounded scroll and selection control — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this establishes a substantive package-private TUI interaction contract across three existing surfaces without changing a public API, persistence boundary, protocol, or system architecture.
**Decision record:** None — the reusable control remains confined to `cmd/mecatui/ui`; its local ownership and interaction choices belong in this plan rather than a durable architecture record.
**Phase:** bounded scroll-selection convergence, slice 1
**Status:** proposed, 2026-09-15. Initial slice of the incremental migration tracked by #1589.
**Delivery:** Split. Shared cursor, layout, style, and pointer semantics need human interface review before implementation changes multiple surfaces.
**Expected tasks:** 3
**Issue:** [stacklok/mecatl#1589](https://github.com/stacklok/mecatl/issues/1589).

Mecatui will gain one package-private bounded-window primitive and a selectable-list layer over it. The window supports browsing rendered physical lines within explicit width and height bounds; the list adds an optional logical-item cursor whose rows may span multiple physical lines. This first slice proves the contract in the unified Agents overlay, the `/models` picker, and the browsing-only `/skills` inventory. Filtering, actions, domain state, confirmation, and data loading remain owned by each surface.

This is an incremental targeted correction, not the future formal migration of every TUI surface. The specialized conversation viewport remains unchanged: it owns streaming-tail following, logical text selection, reflow anchors, and drag selection under [ADR 0301](../adr/0301-logical-conversation-anchors.md). The client-only package boundary remains the one documented in [`docs/architecture.md`](../architecture.md).

## Human decisions

- [x] Initial consumers — Decision: prove multiline selection in the unified Agents overlay, fixed-height selection in `/models`, and browsing-only rendered-line scrolling in `/skills`; defer all other surfaces to independently classified follow-ups under #1589.
- [x] Horizontal behavior — Decision: width is a hard rendering bound with a caller-selected wrap-or-clip policy; this slice adds no horizontal panning or horizontal-scroll input.
- [x] Cursor presentation — Decision: selectable rows use the literal `▶ ` cursor marker with unbordered `accent` styling and an equal-width unselected prefix; text selection, control focus, current `●`, and default `★` retain distinct meanings and styles.
- [x] Pointer behavior — Decision: wheel input moves a selectable list by one logical item and a browsing window by one physical line; a primary click on any visible physical line of a selectable item moves the cursor but does not activate it; Enter remains activation; hover styling is deferred.

## Interface contract

- **gRPC / protobuf:** None — all affected state and interactions remain inside the proto-free mecatui renderer.
- **Exported Go APIs / interfaces:** None — the bounded window and selectable-list layer are package-private concrete helpers under `cmd/mecatui/ui`; the conversation-view controller and public engine surface are unchanged.
- **Tool schemas:** None — no model-facing tool or argument changes.
- **CLI / config:** None — no flags, settings, key-binding names, or defaults change; existing remappable navigation actions remain authoritative where a surface already supports them.
- **Events / persistence:** None — cursor, offset, geometry, and pointer-hit state remain ephemeral client state and add no event or persisted representation.
- **Security / authority:** None — the control renders only content already admitted to its owning surface and preserves frame-scoped opaque hit dispatch; it introduces no new content, trust, permission, or ownership path.
- **Compatibility / migration:** The unified Agents overlay, `/models`, and `/skills` adopt the package-private control in this slice. Agents and Models cursor rows converge on `▶ ` plus unbordered `accent`; wheel input is consumed by the visible surface; Models and visible Agents rows gain click-to-cursor without click-to-activate. Skills keeps browsing-only semantics. Conversation scrolling and every unlisted surface remain unchanged.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — bounded multiline browsing and selection

A surface supplies explicit content width and physical-line height together with logical items. The shared window accounts for wrapped or clipped physical lines, while its optional list layer keeps a logical cursor item visible without splitting it when that item can fit. This generalizes the cursor-following physical budget introduced for the unified Agents overlay without broadening the conversation controller isolated by [ADR 0301](../adr/0301-logical-conversation-anchors.md).

**Acceptance:**
- AC1.1: At positive width and height, rendered output occupies no more than the supplied physical-line height and no rendered line exceeds the supplied display-cell width after ANSI styling is removed.
  - verify: `TestMecatuiBoundedScrollSelection_Scenario1_RespectsWidthAndHeight`
- AC1.2: In selection mode, Up/Down moves by one logical item, Page Up/Page Down uses the current physical-line capacity, Top/End clamps to the first/last item, and a multiline cursor item remains wholly visible whenever its own rendered height fits the window.
  - verify: `TestMecatuiBoundedScrollSelection_Scenario1_MultilineCursorWindow`
- AC1.3: In browsing mode, line and page movement clamp one physical-line offset against the current rendered content, and resizing or replacing content clamps stale offsets without producing blank reachable pages.
  - verify: `TestMecatuiBoundedScrollSelection_Scenario1_BrowsingWindowClamps`
- AC1.4: Zero or negative available width or height produces an empty bounded body and no panic; this slice exposes no horizontal offset or panning action.
  - verify: `TestMecatuiBoundedScrollSelection_Scenario1_DegenerateBounds`

### Scenario 2 — consistent cursor semantics across representative surfaces

A user navigating Agents or Models sees one cursor treatment independent of the row's domain status, while Skills remains a browsing window with no fabricated selection. Current/default markers retain their separate meanings. The implementation preserves the package boundary for the proto-free TUI documented in [`docs/architecture.md`](../architecture.md) and the targeted scope discipline in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC2.1: Selected unified-Agent and Model rows begin with `▶ ` and use unbordered `accent` styling; unselected rows reserve the same marker width, and wrapping a selected row adds no border, padding, background-derived line, or clipped continuation.
  - verify: `TestMecatuiBoundedScrollSelection_Scenario2_CursorStyleConverges`
- AC2.2: Model current `●` and global-default `★` markers remain visible and independent of the cursor, and the Agents active-tab and Parallel-winner markers remain distinguishable from it.
  - verify: `TestMecatuiBoundedScrollSelection_Scenario2_StatusMarkersRemainIndependent`
- AC2.3: The Skills inventory uses browsing mode with its existing content and actions, gains no cursor marker or row activation, and remains bounded after width/height changes.
  - verify: `TestMecatuiBoundedScrollSelection_Scenario2_SkillsRemainsBrowsingOnly`
- AC2.4: The command palette, mentions, Sessions, conversation viewport, and all other unlisted surfaces retain their existing rendering and interaction behavior in this slice.
  - verify: inspection — the implementation changes only the shared helper and the three named consumer families because later adoption is tracked separately by #1589.

### Scenario 3 — visible surfaces own consistent mouse input

In alternate-screen mouse mode, wheel and primary-click input target the visible control rather than hidden conversation content. Existing frame-scoped hit regions remain authoritative, as documented for dynamic surfaces in [`docs/design/IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md).

**Acceptance:**
- AC3.1: A wheel event over Models moves its cursor by one logical item; over an Agents roster it moves that roster cursor by one logical item; over an Agents detail or Skills inventory it moves the browsing offset by one physical line, with every path clamped at both ends.
  - verify: `TestMecatuiBoundedScrollSelection_Scenario3_WheelUsesOwningControl`
- AC3.2: Wheel input while Agents, Models, or Skills owns the body never changes the hidden conversation viewport offset, including when the owning control is already at its boundary.
  - verify: `TestMecatuiBoundedScrollSelection_Scenario3_WheelNeverLeaksToConversation`
- AC3.3: A primary click on any visible physical line of a multiline Agents item or a Model row moves the logical cursor to that item but does not focus, open, cancel, switch models, or otherwise activate the row; Enter retains the surface's existing activation behavior.
  - verify: `TestMecatuiBoundedScrollSelection_Scenario3_ClickSelectsEnterActivates`
- AC3.4: Hit regions are valid only for the render frame and geometry that produced them; an old-frame, out-of-bounds, browsing-only, or closed-surface hit cannot change cursor or activate an action.
  - verify: `TestMecatuiBoundedScrollSelection_Scenario3_StaleAndNonSelectableHitsIgnored`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Command palette, mentions, Sessions, Worktrees, Schedule, MCP, Connect, User Model, Reflections, Dream, Help, Soul, agent inventory, approval, and other detail views | Follow-up slices under #1589 | Classify each batch independently after this control is established; mechanical adoption may be Routine, while new interaction decisions remain Bounded. |
| Main conversation viewport, text-selection behavior, streaming-tail following, or virtualization | ADR 0301 and a future profiling-backed design | Do not fold transcript ownership or horizontal panning into this package-private list/window control. |
| Formal migration of every legacy overlay onto the `surface` lifecycle | Future TUI-system design | This slice may route only the input needed by the named consumers; it does not establish a broad modal framework. |
| Hover styling, pointer-driven activation, drag selection of rows, touch gestures, or horizontal panning | Later interaction work with a concrete use case | Mouse support in this slice is wheel plus click-to-cursor; activation remains explicit through Enter. |
| Filtering, server pagination, actions, confirmation, data loading, or domain state | Existing surface owners | The shared control owns only bounds, offsets, cursor visibility, rendering metadata, and pointer targets. |

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, `task site:build`, and `task api:check` gates pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Private helper names and storage layout are implementation details. Keep the abstraction concrete and package-private; do not introduce a general widget framework or exported interface.
- ANSI-aware display width and multiline hit geometry must share the same rendered-row accounting. Computing hit regions from a second wrapping path risks cursor and pointer disagreement.
- The Agents overlay is not yet a formal `surface`. Its targeted wheel/click routing must not become an implicit broad migration or allow pointer input to reach hidden conversation content.
