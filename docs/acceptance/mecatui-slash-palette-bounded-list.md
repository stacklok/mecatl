# Mecatui slash-command palette bounded list — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — the existing inline palette adopts the established package-private bounded-list interaction contract, changing cursor state, physical-row geometry, and selection presentation without changing a public API, persistence boundary, protocol, or system architecture.
**Decision record:** None — this is a focused application of the landed Mecatui bounded-control contract; its surface-local behavior belongs in this plan rather than a durable architecture record.
**Phase:** bounded-list adoption, slash-command palette slice
**Status:** proposed, 2026-09-23. Ready for Plan / Interface review.
**Delivery:** Split. The palette has distinct inline-filtering, built-in dispatch, completion, and layout behavior that needs its interaction contract reviewed before implementation.
**Expected tasks:** 1
**Issue:** [stacklok/mecatl#1742](https://github.com/stacklok/mecatl/issues/1742).

This first #1742 implementation slice migrates only the slash-command palette to the reusable `bounded.List` control. The palette remains an inline dropdown above the prompt: typing continues to edit the prompt, command filtering remains palette-owned, and the established completion, built-in dispatch, lazy discovery, and Escape-dismissal behavior remain unchanged. The separate `@` mention menu is a later slice, not bundled into this change.

The existing palette has a local numeric cursor and a fixed logical-row `scrollWindow` projection in [`cmd/mecatui/ui/palette.go`](../../cmd/mecatui/ui/palette.go). The replacement follows the pointer-owned, stable-ID, ANSI-aware physical-row contract already established by [Mecatui bounded scroll and cursor control](mecatui-bounded-scroll-selection.md) and [`cmd/mecatui/ui/internal/bounded`](../../cmd/mecatui/ui/internal/bounded). It does not extend that control or create a new widget framework.

## Human decisions

- [x] Palette fitting policy — Decision: use `bounded.Wrap`, preserving complete command descriptions across bounded physical rows. The physical-row budget, overflow indicators, and cursor movement follow that wrapped layout; the existing remappable `ScrollU`/`ScrollD` actions page through an oversized selected command's physical segments.
- [x] Short-terminal behavior — Decision: derive the palette body height from the rows remaining after preserving the prompt and footer, capped at eight physical command-content rows. When no command-content row fits, suppress the entire palette card for that frame; typing continues to filter and opening the palette again on a larger frame restores it.

## Interface contract

- **gRPC / protobuf:** None — palette discovery continues to use the existing `ListCommands` client path without changing messages, RPCs, or compatibility.
- **Exported Go APIs / interfaces:** None — the work consumes the existing package-private `cmd/mecatui/ui/internal/bounded.List`; it adds no exported Go symbol or interface.
- **Tool schemas:** None — no model-facing tool name, input, output, or instruction changes.
- **CLI / config:** None — no flag, setting, key-binding name, or default changes. While the palette body is visible, the existing remappable `ScrollU` and `ScrollD` actions page through a wrapped selected command; Up/Down, Tab/Enter, and Escape retain their existing palette semantics. Home, End, mouse, and click interactions are not added.
- **Events / persistence:** None — filtered commands, cursor selection, list anchors, and geometry remain ephemeral client state.
- **Security / authority:** None — the palette renders the existing built-in and server-discovered command records after their present capability/wiring gates. It introduces no new command discovery, execution, permission, trust, or secret path.
- **Compatibility / migration:** The slash palette replaces its local cursor/window projection with a pointer-owned `bounded.List`. Command IDs are stable and collision-free across compatible filtering and command-refresh updates. The palette keeps its existing input-driven opening, filtering, lazy fetch-once behavior, built-in precedence and dispatch, workspace completion, and Escape dismissal. Standard selectable rows use `presentListRow` with the one-cell selection gutter and the established unbordered selected-row style; `askButtonActive` remains reserved for permission buttons. While visible, the palette additionally owns the existing `ScrollU`/`ScrollD` actions for page traversal of a wrapped selected command. When no command-content row fits and the card is suppressed, it relinquishes all palette navigation, completion, and dismissal keys to ordinary phase/prompt handling. The `@` mention menu, Sessions, and every other #1742 surface remain unchanged in this slice.

## In scope — 1 scenario, in implementation order

### Scenario 1 — slash-command palette preserves input behavior while bounding selectable rows

When a prompt contains a single-line slash-command token, the palette projects its filtered built-in and discovered commands through `bounded.List`. The list receives the available card content width and a body height selected by the fitting and short-terminal decisions above, capped at eight physical command-content rows. `bounded.List` owns only selection, anchors, and physical layout; the palette continues to own command identity construction, filtering, card chrome, hint wording, activation, and phase-specific key routing. This follows the package boundary and row-presentation decision in [the landed bounded-control plan](mecatui-bounded-scroll-selection.md#human-decisions), the client-only UI boundary in [`docs/tui.md`](../tui.md), and the `AGENTS.md` requirement to preserve stateful pointer-owned list controls and surface-owned activation in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC1.1: At usable narrow, normal, and short geometry, the open palette follows the approved fitting policy, renders no more than eight physical command-content rows, keeps every rendered row within the offered display-cell width, and exposes accurate above/below overflow indicators. The complete inline palette region follows the approved short-terminal policy and cannot displace the prompt or footer beyond the offered frame; degenerate list geometry renders no list body without panic.
  - verify: `TestMecatuiSlashPaletteBoundedList_Scenario1_GeometryAndIndicators`
- AC1.2: Up and Down move exactly one logical command and minimally reveal it. `ScrollU` and `ScrollD` expose every contiguous physical segment of a selected command whose wrapped description exceeds the body height before moving to the preceding or following command. Re-filtering or refreshing the merged command set retains the selected command by stable ID when it remains available; when it disappears, the list adopts the clamped replacement without later snapping back.
  - verify: `TestMecatuiSlashPaletteBoundedList_Scenario1_SelectionAnchors`
- AC1.3: Every visible command row is rendered through `presentListRow` with one selection cell, an equal-width blank unselected gutter, and the palette's selected/unselected row styles. The selected row does not use the padded, bordered permission-button `askButtonActive` treatment; descriptions remain readable under every built-in theme.
  - verify: `TestMecatuiSlashPaletteBoundedList_Scenario1_StandardRowPresentation`
- AC1.4: While idle, typing still edits and filters the prompt; Tab or Enter still dispatches an eligible built-in or completes a workspace command; and Escape still hides the palette until command mode is left. While a run streams, the palette continues to own Up, Down, `ScrollU`, `ScrollD`, Tab, and Enter, but Escape remains the run-cancel action rather than dismissing the palette. When short geometry suppresses the palette card, it claims none of those navigation, completion, or dismissal keys and ordinary phase/prompt handling receives them. Command discovery still runs at most once per session; no Home, End, mouse, or click interaction is claimed by the palette.
  - verify: `TestMecatuiSlashPaletteBoundedList_Scenario1_PreservesInteractionOwnership`
- AC1.5: The owning Mecatui guidance explains the palette's implemented selection, overflow, and `ScrollU`/`ScrollD` behavior without claiming unsupported jump or pointer controls.
  - verify: inspection — update `user-docs/mecatui/using-the-tui.md` and `user-docs/mecatui/keybindings.md` in the implementation PR, then pass `task docs` and `task site:build`.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| `@` mention completion menu | A separate #1742 acceptance-plan slice | It remains a distinct inline interaction and is not mechanically coupled to the palette. |
| Sessions, Worktrees, Schedule, MCP, Connect, Skills, User Model, Reflections, and other selectable inventories | Later #1742 slices | Each surface is classified and planned independently when its interaction contract is selected. |
| Read-only detail panes | Later #1742 viewport slices | Use `bounded.Viewport` only where physical-line browsing, not logical selection, is the required interaction. |
| Main conversation viewport, text selection, and streaming-tail behavior | Existing conversation ownership work | Excluded by the bounded-list adoption scope. |
| New palette jump, pointer, or mouse interactions | A later explicitly planned interaction change | Preserve explicit keyboard selection and activation; this slice adds only `ScrollU`/`ScrollD` page traversal required for wrapped oversized command descriptions. |

## Definition of done

1. Focused palette and bounded-control tests, `task lint`, `task test`, and `task test:race` pass.
2. `task docs` and `task site:build` pass after updating the owning Mecatui guidance.
3. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green for the runtime change.
5. The implementation PR links this approved Plan / Interface PR and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The exact `paletteState` field layout and helper names remain implementation details. The list must be pointer-owned so Bubble Tea value-model copies retain one control state, as required by `bounded.List`.
- Use the existing collision-free list-identity convention for command IDs; do not derive identity from rendered text or a numeric filtered index.
- Keep palette card borders, headers, hints, empty-match note, terminal sanitization, and command activation local. The bounded package remains theme- and Bubble Tea-free.
- User-supplied themes can choose arbitrary palette colors, so tests prove semantic style selection and built-in-theme rendering rather than claiming a universal numeric contrast ratio for arbitrary theme JSON.
