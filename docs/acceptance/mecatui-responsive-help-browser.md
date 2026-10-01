# Mecatui responsive Help browser — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this changes the local, user-visible geometry and physical-row browsing behavior of the existing Help overlay without changing a public API, persistence boundary, protocol, security boundary, or system architecture.
**Decision record:** None — the root-owned Help state, local viewport integration, and card geometry remain confined to `cmd/mecatui/ui`; their rationale belongs in this plan rather than a durable architecture record.
**Phase:** bounded-browser adoption, Help slice
**Status:** proposed, 2026-10-01. Retroactively records the directing human's decisions after an authorized UI spike validated the interaction.
**Delivery:** Split. The user-visible width, wrapping, compact fallback, and keyboard-ownership contract need plan/interface review before the implementation candidate can merge.
**Expected tasks:** 1
**Issue:** [stacklok/mecatl#1900](https://github.com/stacklok/mecatl/issues/1900).
**Plan PR:** [#2045](https://github.com/stacklok/mecatl/pull/2045)

The root-owned `?` and `/help` overlay will replace its local complete-line window and numeric scroll accounting with the existing `bounded.Viewport` using physical-row wrapping. It remains a read-only local Help browser, not a generic `surface` modal. The viewport receives caller-provided independently ANSI-styled Help lines and owns only wrapped-row geometry, offset, movement, clamping, and projection; Help retains its content, live-binding indicator, card chrome, lifecycle, and input routing.

At usable positive geometry, the Help card's outer display width is `min(offered width, max(69, floor(80% of offered width)))`. The card renderer applies that width independently of the currently visible content, then derives the Viewport body width by subtracting the measured `askCard` frame. Narrower content changes only wrapping and the reachable physical rows; it cannot make the card shrink or expand while scrolling.

## Human decisions

- [x] Ownership and control — Decision: Help remains a root-owned, read-only overlay. It replaces only its local numeric physical-row window with root-owned `bounded.Viewport`; it does not migrate into the generic `surface` lifecycle or add selection, activation, filtering, click behavior, or a new modal owner.
- [x] Geometry and width — Decision: the normal card uses outer width `min(offered width, max(69, floor(80% of offered width)))`, and the renderer applies that width before centering. The Viewport body width subtracts the measured `askCard` horizontal frame. The existing one-line unframed fallback remains when positive height cannot fit card chrome; unknown/nonpositive geometry retains the existing bare-card behavior.
- [x] Browsing and lifecycle — Decision: wrap Help's existing independently styled complete lines into physical rows. Preserve live-binding Help indicator/chrome, close/refocus behavior, root key precedence, configured line/page/top/end bindings, and resize clamping. Both `?` and `/help` reset browsing to the first physical row. While Help is visible, wheel up/down moves its viewport one physical row and is consumed at every offset and compact/non-overflow state, so it cannot reach the hidden conversation.
- [x] Documentation — Decision: update the existing [Using the TUI](../../user-docs/mecatui/using-the-tui.md) reference as the sole public owner to describe responsive, scrollable Help without duplicating its implementation policy.

## Interface contract

- **gRPC / protobuf:** None — Help remains a local client rendering and adds no RPC, message, field, or wire-compatibility change.
- **Exported Go APIs / interfaces:** None — the change consumes the package-private `cmd/mecatui/ui/internal/bounded.Viewport` and adds no exported symbol, interface, or external Go API.
- **Tool schemas:** None — model-facing tools, schemas, instructions, and execution behavior remain unchanged.
- **CLI / config:** None — no command, flag, setting, or binding name/default changes. Existing remappable Help, Close, Up, Down, ScrollU, ScrollD, ScrollTop, and ScrollBottom actions retain their Help behavior.
- **Events / persistence:** None — Help geometry and viewport offset are ephemeral client UI state; no event, durable state, or migration is introduced.
- **Security / authority:** None — Help remains local read-only presentation of client-owned capability and keybinding information; terminal-safe existing Help rendering and input ownership remain in force.
- **Compatibility / migration:** Replace Help's local complete-line offset/window projection with a root-owned wrapped physical-row viewport. Preserve Help content, live bindings, chrome, keyboard behavior, close/refocus behavior, and tiny-height fallback. At positive widths, card width intentionally changes from content-derived sizing to the recorded stable responsive formula; while Help is visible, wheel events intentionally move Help rather than the hidden conversation. Public Help text continues to be available through the existing overlay and `/help` command.

## In scope — 2 scenarios, in implementation order

### Scenario 1 — stable responsive Help card

When the Help overlay is open, its card stays centered and has a stable geometry-derived outer width, regardless of the current scroll position or capability/keybinding-dependent Help content. Its body wraps existing styled Help lines within the measured inner card width. This follows the existing bounded-control approach in [Mecatui bounded scroll and cursor control](mecatui-bounded-scroll-selection.md) while preserving the surface-specific ownership required by [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC1.1: At 40, 69, 100, and 200 offered columns, the Help card outer width is `min(offered width, max(69, floor(80% of offered width)))`; the body derives its width by subtracting the measured `askCard` frame.
  - verify: `TestHelpCardWidthIsTerminalBounded`
- AC1.2: At narrow and ordinary positive widths, no ANSI-stripped Help row exceeds the offered width, and the rendered outer-card width is unchanged between its initial frame and its End-scrolled frame.
  - verify: `TestHelpFitsAvailableWidth`
- AC1.3: Dynamic Help rows, including long capability-dependent copy and live bindings, wrap within the fixed card rather than determining or changing its width.
  - verify: `TestHelpOverlayEmbeddedGolden`, `TestHelpOverlayAllOnGolden`

### Scenario 2 — reachable wrapped Help and retained overlay behavior

The Help browser supplies its existing independently styled body lines to the viewport as caller-owned content. It preserves Help's own indicator and compact behavior while navigating physical wrapped rows through the current root input route. This does not broaden Help into the generic `surface` framework, preserving the local ownership boundary in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC2.1: Wrapped Help content is reachable through configured one-row, page, Top, and End navigation; End reveals the final wrapped row and resize clamps retained position without forcing an existing End selection to follow a new bottom.
  - verify: `TestHelpWrapsNarrowBodyAndNavigatesWrappedRows`, `TestHelpScrollNavigationAndReset`, `TestHelpScrollClampsAfterResizeWithoutFollowingEnd`
- AC2.2: The overflow indicator remains Help-owned, shows live configured bindings, reserves only the rows it needs, and the normal card never exceeds its offered height.
  - verify: `TestHelpRenderingIsHeightBoundedAndShowsScrollGuidance`, `TestHelpNavigationRespectsKeyOverrides`
- AC2.3: Help retains its lifecycle and global-key precedence: `?` opens only from an empty prompt, `?`/Close restores prompt focus, `/help` and `?` reset browsing, and quit/suspend behavior remains globally available.
  - verify: `TestHelpOpensOnlyOnEmptyInput`, `TestHelpPreservesGlobalLifecycleKeys`, `TestHelpScrollNavigationAndReset`
- AC2.4: At heights unable to fit card chrome, Help retains its existing usable single-line fallback; unknown/nonpositive geometry retains the bare-card path.
  - verify: `TestHelpRenderingIsHeightBoundedAndShowsScrollGuidance`
- AC2.5: While Help is visible, wheel up/down moves its wrapped viewport exactly one physical row and is consumed at both endpoints and in compact/non-overflow states; it cannot move the hidden conversation viewport, selection, or prompt state.
  - verify: `TestHelpWheelOwnershipAndPhysicalRowBrowsing`
- AC2.6: The existing public [Using the TUI](../../user-docs/mecatui/using-the-tui.md) page describes that Help adapts to terminal size and can be navigated with the displayed bindings, without documenting viewport internals or duplicating the complete live binding table.
  - verify: inspection — update the existing owner, then pass `task docs` and `task site:build`.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Migration of Help into the generic `surface` lifecycle or a full-screen modal | Future TUI-system design | Responsive wrapping does not justify changing root overlay ownership. |
| New Help actions, selection, filtering, editing, click handling, horizontal panning, or changes to keybinding defaults | Future Help interaction work | This slice preserves the current keyboard contract and adds only the existing bounded-overlay wheel ownership pattern. |
| A shared cross-overlay width token or refactor of other card geometries | Independently classified TUI consistency work | The 69-column/80-percent rule is a Help-local product decision. |
| Changes to Help content, capability advertisement, command availability, or server behavior | Existing content and capability owners | This slice only changes local Help presentation and browsing. |

## Definition of done

1. Focused Help, bounded-viewport, and UI geometry tests pass, followed by `task lint`, `task test`, and `task test:race` on the final implementation candidate.
2. `task docs` and `task site:build` pass after updating the sole owning Help reference.
3. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green for the runtime change.
5. The implementation PR links this approved Plan / Interface PR and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Private helper, field, and fixture names remain implementation details. Do not retain the old numeric complete-line scroll path as a competing offset authority after migration.
- The stable width rule is intentionally Help-local. A later shared card-width policy must independently classify its affected surfaces rather than coupling this overlay to unrelated UI geometry.
- Indicator wrapping can consume more than one row at narrow widths; the implementation must measure and reserve its actual wrapped height before projecting Help content.
- The plan preserves existing compact-height behavior. A redesign of Help for exceptionally short terminals is separate interaction work.
