# Mecatui agent-definition inventory bounded viewport — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this changes the local, user-visible geometry and browsing contract of one existing mecatui inventory without changing a public API, persistence boundary, protocol, security boundary, or system architecture.
**Decision record:** None — the pointer-owned viewport, centered-card geometry, and legacy-overlay ownership remain confined to `cmd/mecatui/ui`; their rationale belongs in this plan rather than a durable architecture record.
**Phase:** bounded-inventory adoption, agent-definition inventory slice
**Status:** proposed, 2026-09-25. The directing operator selected the responsive centered-card and 128-column policy.
**Delivery:** Split. The user-visible geometry, compact fallback, and keyboard-ownership contract need plan/interface review before implementation.
**Expected tasks:** 1
**Issue:** [stacklok/mecatl#1897](https://github.com/stacklok/mecatl/issues/1897).
**Plan PR:** pending

`/agents` will replace its duplicated physical-row offset and fixed fourteen-line window with the established pointer-owned `bounded.Viewport`. It remains a read-only, idle-only definition inventory over the conversation, not the live `/team` view and not a `surface`-owned modal. The viewport owns physical layout and offset over the overlay's supplied rendered lines; the overlay retains the snapshot RPC, state copy, terminal-safe row construction, chrome, and input lifecycle.

The card will use the offered conversation region rather than a fixed body budget: at normal geometry it is centered and capped at 128 outer display cells, then its viewport receives every usable physical body row after measured card frame, title, spacing, overflow indicator, and footer. At short positive geometry where that chrome plus one body row cannot fit, it shows a width-clipped close-only compact fallback; nonpositive geometry renders nothing. This intentionally supersedes the prior follow-on recommendation to retain the fourteen-line budget. The closest control contract is [Mecatui bounded scroll and cursor control](mecatui-bounded-scroll-selection.md); its deferred agent-inventory scope requires this independently reviewed slice.

## Human decisions

- [x] Ownership and control — Decision: `agentsInvState` remains the legacy root-owned overlay state. It replaces only its numeric physical offset with a pointer-owned `bounded.Viewport`; no `surface` migration, modal registration, selection, activation, filtering, refresh action, or live-team behavior is introduced. `bounded.Viewport` receives caller-provided ANSI-safe physical lines and owns only geometry, offset, movement, clamping, and projection.
- [x] Geometry and width — Decision: retain the centered `askCard` presentation, cap its outer width at `min(128, offered width)`, and derive its text width by subtracting the current card's measured horizontal frame. At normal positive geometry, derive one physical body-height budget from the offered conversation height after all rendered card chrome, including any overflow indicator, then use that same budget for viewport projection and input clamping. Do not reuse the unrelated 132-column permission-modal constant; future cross-surface alignment is out of scope. If fixed chrome plus one body line cannot fit, show only a width-clipped close action; nonpositive geometry renders nothing.
- [x] Browsing and lifecycle — Decision: preserve current read-only, idle-only, one-request-per-open snapshot behavior; each open has UI-private generation identity, and only its still-open request may replace definitions, clear state, or reset browsing to the top. Escape closes and refocuses the prompt. Configured Up/Down and ScrollU/ScrollD move one physical line; configured ScrollTop/ScrollBottom move to endpoints. Every other key, including activation keys, remains swallowed, and no wheel or pointer behavior is added.
- [x] Documentation — Decision: update the existing `/agents` entry in [`docs/tui.md`](../tui.md) as the sole current behavior reference, replacing its fixed-line-window claim with responsive geometry and compact close-only behavior. No public `user-docs/` page currently documents this implementation-level inventory layout.

## Interface contract

- **gRPC / protobuf:** None — `/agents` continues to use the existing `ListAgents` client call and existing response fields; no message, RPC, field, or compatibility change is made.
- **Exported Go APIs / interfaces:** None — the work consumes the existing import-restricted concrete `cmd/mecatui/ui/internal/bounded.Viewport`; it adds no exported symbol, Go interface, or external API.
- **Tool schemas:** None — model-facing tool names, inputs, outputs, instructions, delegation, and agent-definition resolution remain unchanged.
- **CLI / config:** None — no command, flag, setting, or binding name/default changes. Existing remappable Close, Up, Down, ScrollU, ScrollD, ScrollTop, and ScrollBottom actions remain authoritative while the normal overlay is visible; the compact fallback recognizes Close only.
- **Events / persistence:** None — viewport offset and geometry remain ephemeral UI state. The resolved agent-definition snapshot, its discovery cadence, and any durable state retain their existing owners and formats.
- **Security / authority:** None — the overlay remains a read-only rendering of already-admitted definition metadata. Preserve terminal sanitization before styling, capability-derived empty-state wording, idle-only opening, and the distinction between definition discovery and delegation activation.
- **Compatibility / migration:** Replace the local numeric scroll/window accounting with one pointer-owned viewport and a single measured geometry path. Preserve centered overlay placement, per-open snapshot replacement/reset semantics while rejecting stale closed or superseded request results, name/description/metadata row anatomy, color-as-layout-neutral rendering, loading/error/empty/disabled states, close/focus behavior, and physical-line navigation. It intentionally changes normal card width from the current centered-card maximum and normal body height from fourteen lines to available geometry; the documented `/agents` reference changes with it.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — responsive, bounded agent-definition card

When an idle client opens `/agents`, the definition inventory uses a centered card whose outer width is no greater than the offered width and no greater than 128 display cells. It computes all normal body rows from the offered conversation geometry after its own chrome, then supplies the same physical budget to the existing bounded viewport control. This applies the physical-layout ownership established in [the bounded-control plan](mecatui-bounded-scroll-selection.md#human-decisions) and the minimum-change discipline in [`AGENTS.md`](../../AGENTS.md), without importing Sessions' selection, action, pagination, or modal lifecycle.

**Acceptance:**
- AC1.1: At narrow, ordinary, and wide positive widths, the centered normal card has no ANSI-stripped line wider than its offered width, has outer width `min(128, offered width)` whenever its normal card can fit, and derives content width by subtracting the measured `askCard` frame rather than a literal frame size.
  - verify: `TestMecatuiAgentInventoryBoundedViewport_Scenario1_WidthCapAndFrameAccounting`
- AC1.2: At short, ordinary, and tall positive offered heights, the complete normal card has no rendered height greater than its offered height and its viewport exposes every usable physical body row after title, spacing, footer, card frame, and visible overflow chrome; rendering and navigation use the same budget.
  - verify: `TestMecatuiAgentInventoryBoundedViewport_Scenario1_UsesAvailableHeightWithOneGeometryPath`
- AC1.3: Every populated, loading, long sanitized-error, enabled-empty, and capability-disabled state has no ANSI-stripped line wider than its offered width and no total card height greater than its offered height; each state uses the measured available body or the close-only compact fallback rather than bypassing the card budget.
  - verify: `TestMecatuiAgentInventoryBoundedViewport_Scenario1_AllStatesFitOfferedGeometry`
- AC1.4: If positive geometry cannot fit fixed chrome and one body line, the overlay renders a width-clipped close-only compact fallback that constructs no viewport or browsable rows; nonpositive geometry renders no overlay.
  - verify: `TestMecatuiAgentInventoryBoundedViewport_Scenario1_CompactFallbackAndNonpositiveGeometry`

### Scenario 2 — safe physical browsing and snapshot replacement

The read-only overlay keeps building its own terminal-sanitized, ANSI-carrying physical rows for definition name, wrapped description, and metadata. Its viewport owns physical-line offset and clamps it after content or geometry changes; successful snapshot replacement intentionally starts readers at the first line, preserving the overlay's current one-shot refresh semantics. This preserves the repository's requirement that file and presentation state stay with their existing owner rather than creating a competing control authority in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC2.1: Long, wrapped definition inventories are completely reachable by physical-line movement, clamp at both endpoints, and never expose a reachable blank page after resize or content replacement.
  - verify: `TestMecatuiAgentInventoryBoundedViewport_Scenario2_ClampsPhysicalBrowsing`
- AC2.2: A successful current-open result replaces the displayed snapshot and resets browsing to its first physical row; a result delivered after close or from a prior open cannot change definitions, loading/error state, or viewport position.
  - verify: `TestMecatuiAgentInventoryBoundedViewport_Scenario2_CurrentOpenOwnsResult`
- AC2.3: Loading, error, enabled-empty, and capability-disabled states construct no stale viewport rows and retain their existing distinct copy.
  - verify: `TestMecatuiAgentInventoryBoundedViewport_Scenario2_NonInventoryStatesClearBrowsing`
- AC2.4: Definition names, descriptions, models, permission modes, and tool names remain terminal-sanitized before styling; color hints remain layout-neutral and multiline wrapped physical rows do not leak ANSI styling across viewport boundaries.
  - verify: `TestMecatuiAgentInventoryBoundedViewport_Scenario2_PreservesSafeRowPresentation`

### Scenario 3 — legacy overlay lifecycle and input ownership

`/agents` remains an idle-only, read-only discovery overlay for the resolved definition registry described by [ADR 0013](../adr/0013-agent-definitions.md). It neither configures nor invokes agents, and it retains its legacy root-owned update path rather than becoming a Sessions-like full-region modal.

**Acceptance:**
- AC3.1: Opening remains dependency- and idle-phase-gated, blurs the prompt, and starts one existing listing request; Escape clears the overlay and restores prompt focus.
  - verify: `TestMecatuiAgentInventoryBoundedViewport_Scenario3_OpenCloseLifecycle`
- AC3.2: Configured Up/Down and ScrollU/ScrollD advance exactly one physical line; configured ScrollTop/ScrollBottom reach the respective endpoint. Tests prove remapped bindings rather than literal default key text.
  - verify: `TestMecatuiAgentInventoryBoundedViewport_Scenario3_RemappablePhysicalLineNavigation`
- AC3.3: Every other key is consumed by the overlay, and no selection, activation, wheel, click, hidden-prompt, hidden-conversation, `/team`, or `surface` behavior is introduced.
  - verify: `TestMecatuiAgentInventoryBoundedViewport_Scenario3_ReadOnlyInputOwnership`
- AC3.4: The existing `/agents` reference describes responsive bounded card geometry and the compact close-only fallback without duplicating viewport implementation details.
  - verify: inspection — update [`docs/tui.md`](../tui.md), then pass `task docs`.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Migration of `/agents` into the `surface` lifecycle or a full-screen Sessions-like command | Future TUI-system design | Responsive sizing alone does not justify changing root overlay ownership or modal routing. |
| Agent selection, activation, editing, filtering, explicit refresh, pagination, definition discovery, or delegation behavior | Existing agent-definition and delegation owners | `/agents` remains a one-shot read-only presentation of the existing resolved snapshot. |
| Wheel scrolling, click/hover/drag/touch input, or smooth-wheel coalescing | #1790 and future interaction work | Preserve the current keyboard-only overlay contract. |
| Permission-modal width or a cross-UI width-token refactor | Later independently classified UI consistency work | This slice records a local 128-column `/agents` policy; it does not couple unrelated surface constants. |
| Main conversation viewport, text selection, streaming-tail behavior, Sessions, Skills, Saved Memory, Help, Dream, Approval, or transcript browsing | #1742 follow-on slices and their existing owners | Each surface has independent interaction and ownership semantics. |

## Definition of done

1. Focused agent-inventory, bounded-control, and UI geometry tests pass, followed by `task lint`, `task test`, and `task test:race`.
2. `task docs` passes after updating the sole owning `/agents` behavior reference.
3. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green for the runtime change.
5. The implementation PR links this approved Plan / Interface PR and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Private field, helper, and test-fixture names remain implementation details. Delete or reuse the local scroll/window logic rather than retaining it as a competing physical-geometry authority.
- The plan deliberately changes the prior fixed fourteen-line card policy. Golden updates must demonstrate that the new responsive normal card, not an incidental terminal height, owns the result.
- The 128-column cap matches the current inline palette outer-width maximum, but this plan creates no shared presentation token. A future consistency change must classify its affected surfaces independently.
- `AgentsMsg` remains on the legacy overlay update path, but the overlay must bind results to the open that issued them. A future surface migration must still explicitly design its broader request lifetime and teardown semantics rather than inheriting them incidentally.
