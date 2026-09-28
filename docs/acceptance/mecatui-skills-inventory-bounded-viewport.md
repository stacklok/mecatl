# Mecatui skills inventory bounded viewport — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this changes the local, user-visible geometry and browsing behavior of the existing mecatui skills panel without changing a public API, persistence boundary, protocol, security boundary, or system architecture.
**Decision record:** None — the responsive card geometry and bounded physical viewport remain confined to `cmd/mecatui/ui`; their rationale belongs in this plan rather than a durable architecture record.
**Phase:** bounded-inventory adoption, skills inventory slice
**Status:** landed in this implementation candidate, 2026-09-28. Authoritative when the implementation PR merges; implementation uses the contract approved in #1973 at `7ab88c5a6aa6242af705ac2480f6e8eae54409e2`.
**Delivery:** Split. The panel's geometry, filter, learned-skill lifecycle controls, and input-ownership contract require plan/interface review before implementation.
**Expected tasks:** 1
**Issue:** [stacklok/mecatl#1898](https://github.com/stacklok/mecatl/issues/1898).
**Plan PR:** [#1973](https://github.com/stacklok/mecatl/pull/1973)

`/skills` will replace its fixed fourteen-line external-inventory window and numeric scroll accounting with the established pointer-owned `bounded.Viewport`. At normal geometry, its existing centered card will use the available conversation height after all rendered chrome, cap its outer width at 128 display cells, and give the same measured physical body budgets to rendering and navigation. At short positive geometry where fixed chrome and the required one- or two-region body rows cannot fit, it will show only a width-clipped close action; nonpositive geometry renders nothing.

This remains the existing modal-owned skills panel, not the root-owned `/agents` overlay. It retains one-shot external `ListSkills` discovery, focused type-to-filter behavior, learned-skill listing/detail/lifecycle actions, request-epoch and generation fencing, and the model-only Skill activation boundary described in [Extensibility](../architecture/extensibility.md). The new viewport owns only the physical rows and offset of the filtered external inventory. It neither selects nor activates external skills; learned-skill selection and actions retain their present owner and contracts.

## Human decisions

- [x] Learned-skill placement and normal-geometry keys — Decision: render learned skills in an independently bounded region below the filtered external viewport. When both regions are nonempty, normal geometry requires two body rows, assigns each one row, then splits any remaining rows as evenly as possible with an odd extra row to the external region; a sole nonempty region receives the full body budget. The external inventory/filter owns initial focus; `tab` moves focus between the external region and learned region. Up/Down, ScrollU/ScrollD, ScrollTop/ScrollBottom, and wheel events act only on the focused region; Enter is meaningful only in the learned region and retains its existing detail behavior. The external region cannot select or activate a skill, and the learned region cannot change the filter. This preserves distinct lifecycle identity while keeping both regions within the responsive card's measured height.

## Interface contract

- **gRPC / protobuf:** None — `/skills` continues to call the existing `ListSkills` and learned-skill lifecycle RPCs with their current request, generation, partition, and response fields; no message, RPC, field, or compatibility change is made.
- **Exported Go APIs / interfaces:** None — the work consumes the existing import-restricted concrete `cmd/mecatui/ui/internal/bounded.Viewport`; it adds no exported symbol, Go interface, or external API.
- **Tool schemas:** None — the model-facing read-only `Skill` tool, its name/asset inputs, progressive-disclosure instructions, and the separate learned-skill lifecycle behavior remain unchanged. The panel remains discovery and operator review UI; it does not activate skills.
- **CLI / config:** None — no command, flag, setting, binding name, or default changes. `tab` transfers normal-panel focus between the external filter/viewport and independently bounded learned region. Existing remappable Up, Down, ScrollU, ScrollD, ScrollTop, and ScrollBottom actions act only on the focused region: external focus moves physical external rows, and learned focus moves/selects learned rows. Each wheel event moves one row in that same focused region. Enter remains a learned-region-only detail action; typeable input continues to filter only while external focus is active.
- **Events / persistence:** None — viewport offset, measured geometry, and the focused filter remain ephemeral UI state. External inventory snapshots, learned-skill partitions, generation/revision correlation, lifecycle receipts, and durable publication retain their current owners and formats.
- **Security / authority:** None — preserve terminal sanitization before styling, server capability gating, idle-only opening, current request-epoch/generation/revision checks, external-skill precedence, and the existing distinction between discovery/review UI and model-driven Skill activation. The compact fallback must not construct a viewport or expose lifecycle actions.
- **Compatibility / migration:** Replace the local fixed `skillsBodyLines` window and numeric external-inventory scroll calculations with one pointer-owned viewport and one measured geometry path. Preserve external name/description filtering and server order; learned-skill cursor/detail/actions; error, loading, empty, disabled, and no-match copy; modal lifecycle; and response fencing. Normal geometry intentionally changes the fixed fourteen-line panel to the available body height and caps its outer width at 128 cells. Update the existing `/skills` reference in [`docs/tui.md`](../tui.md) when that behavior ships.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — responsive, bounded skills card

When an idle client opens `/skills`, the existing modal renders its normal card centered in the offered conversation region. It measures the current `askCard` frame and all rendered title, focused filter, footer, and visible overflow chrome before assigning body rows. When both inventories have rows, each independently bounded region receives at least one row and the remaining body rows are split as evenly as possible, with an odd extra row assigned to the external region; a sole nonempty region receives the full body budget. This applies the physical-layout ownership established by [Mecatui bounded scroll and cursor control](mecatui-bounded-scroll-selection.md#human-decisions) without importing `/agents`' root-owned lifecycle or altering the learned-skill contract in [ADR 0111](../adr/0111-hardened-agent-owned-skill-publication.md).

**Acceptance:**
- AC1.1: At narrow, ordinary, and wide positive widths, the centered normal card has no ANSI-stripped line wider than its offered width, has outer width `min(128, offered width)` whenever normal geometry fits, and derives content width by subtracting the measured `askCard` frame rather than a literal frame size.
  - verify: `TestMecatuiSkillsInventoryBoundedViewport_Scenario1_WidthCapAndFrameAccounting`
- AC1.2: At short, ordinary, and tall positive offered heights, the complete normal card has no rendered height greater than its offered height. When both inventories have rows, each region receives at least one body row and remaining rows are split evenly, with an odd extra row assigned to the external region; a sole nonempty region receives the full body budget. Rendering and navigation use those same measured budgets.
  - verify: `TestMecatuiSkillsInventoryBoundedViewport_Scenario1_UsesAvailableHeightWithOneGeometryPath`
- AC1.3: Loading, long sanitized error, enabled-empty, capability-disabled, filter-no-match, external-inventory, and learned-skill-list states stay within their offered width and height through the same measured layout; they do not bypass the card budget.
  - verify: `TestMecatuiSkillsInventoryBoundedViewport_Scenario1_AllStatesFitOfferedGeometry`
- AC1.4: If positive geometry cannot fit fixed chrome and the required body rows (one for a sole nonempty region or two when both regions are nonempty), the overlay renders a width-clipped close-only compact fallback that constructs no viewport, focused filter, learned-skill selection, detail, or lifecycle action; nonpositive geometry renders no overlay.
  - verify: `TestMecatuiSkillsInventoryBoundedViewport_Scenario1_CompactFallbackAndNonpositiveGeometry`

### Scenario 2 — safe physical browsing, filtering, and inventory replacement

The panel keeps building its own terminal-sanitized, ANSI-carrying physical rows for the filtered external inventory. Its viewport owns physical-line offset and clamps after content, filtering, or geometry changes. A successful current-open external snapshot intentionally resets browsing to its first physical row. The existing model-lifetime request epoch and learned-skill generation/revision checks continue to reject delayed results, as required by [ADR 0111](../adr/0111-hardened-agent-owned-skill-publication.md).

**Acceptance:**
- AC2.1: Long wrapped filtered external inventories are completely reachable by physical-line movement, clamp at both endpoints, and never expose a reachable blank page after resize, query changes, or current snapshot replacement.
  - verify: `TestMecatuiSkillsInventoryBoundedViewport_Scenario2_ClampsPhysicalBrowsing`
- AC2.2: A successful current-open `ListSkills` result replaces the external snapshot and resets its browsing to the first physical row; a response after close or from an older open cannot change external inventory, loading/error state, filter projection, or viewport position.
  - verify: `TestMecatuiSkillsInventoryBoundedViewport_Scenario2_CurrentOpenOwnsExternalResult`
- AC2.3: Loading, error, enabled-empty, capability-disabled, and filter-no-match states construct no stale external viewport rows and retain their existing distinct copy.
  - verify: `TestMecatuiSkillsInventoryBoundedViewport_Scenario2_NonInventoryStatesClearBrowsing`
- AC2.4: External names and descriptions remain terminal-sanitized before styling; wrapped physical rows do not leak ANSI styling across viewport boundaries. Filtering remains case-insensitive over name and description, preserves server order, and does not turn typed `j` or `k` into navigation.
  - verify: `TestMecatuiSkillsInventoryBoundedViewport_Scenario2_PreservesSafeFilteredRows`
- AC2.5: Learned-skill list/detail results retain their existing request-epoch, partition-generation, selected identity/version, expected-revision, and publication-status fencing; this migration does not let a stale learned response replace a newer lifecycle state.
  - verify: `TestMecatuiSkillsInventoryBoundedViewport_Scenario2_PreservesLearnedLifecycleFencing`

### Scenario 3 — modal lifecycle and input ownership

`/skills` remains the existing idle-only modal-owned inventory. External skills remain read-only discovery entries whose activation is model-driven; learned-skill detail and lifecycle actions remain the existing explicit review controls. The normal panel owns its navigation and wheel input so the hidden conversation does not move, while the compact fallback is intentionally static.

**Acceptance:**
- AC3.1: Opening remains dependency- and idle-phase-gated, blurs the prompt, starts the existing external listing and optional learned listing requests, and focuses filtering only in normal panel geometry. Closing restores prompt focus; an Escape on a non-empty filter clears it before a subsequent Escape closes.
  - verify: `TestMecatuiSkillsInventoryBoundedViewport_Scenario3_OpenCloseAndFilterLifecycle`
- AC3.2: `tab` moves focus between the external filter/viewport and learned region. Configured Up/Down bindings advance one physical external line under external focus and one learned row under learned focus; configured ScrollU/ScrollD page the focused region by its current height; configured ScrollTop/ScrollBottom reach that region's endpoint. Tests prove remapped bindings rather than literal default key text, while typed `j`/`k` continue to enter the filter only under external focus.
  - verify: `TestMecatuiSkillsInventoryBoundedViewport_Scenario3_RemappablePhysicalLineAndPagingNavigation`
- AC3.3: Each wheel event moves one row in the normal panel's focused region without changing the other region's selection and cannot move the hidden conversation viewport; at either endpoint it remains consumed. Compact fallback consumes wheel without constructing or moving a viewport.
  - verify: `TestMecatuiSkillsInventoryBoundedViewport_Scenario3_WheelOwnershipAndCompactIsolation`
- AC3.4: Learned-skill cursor, Enter-to-detail, detail Escape, and the existing activate/reject/archive/diff/rollback commands remain available only in normal geometry and retain their current lifecycle RPC behavior; no external-skill selection, activation, click, pointer, or hidden-prompt/conversation behavior is introduced.
  - verify: `TestMecatuiSkillsInventoryBoundedViewport_Scenario3_PreservesLearnedControlsWithoutExternalActivation`
- AC3.5: The existing `/skills` reference describes responsive bounded-card geometry, physical paging, wheel ownership, focused filtering, learned-skill controls, and compact close-only behavior without duplicating viewport implementation details.
  - verify: inspection — update [`docs/tui.md`](../tui.md), then pass `task docs`.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Changes to Skill, ListSkills, learned-skill, or lifecycle RPC/protobuf contracts | Existing skills and learning owners | This is a client-local presentation migration. |
| Skill activation, external-skill selection, editing, refresh, pagination, or changed filter semantics | Existing Skill tool and skills-panel owners | `/skills` remains discovery/review UI; activation stays model-driven. |
| Learned-skill domain lifecycle, publication, CAS, generation/revision semantics, or authority | [ADR 0111](../adr/0111-hardened-agent-owned-skill-publication.md) | The migration preserves these contracts. |
| Smooth-wheel burst coalescing, click/hover/drag/touch input, or horizontal panning | [#1790](https://github.com/stacklok/mecatl/issues/1790) | Adopt only the existing per-event physical wheel behavior. |
| Main conversation viewport, text selection, streaming-tail behavior, Sessions, Saved Memory, Help, Dream, Approval, `/agents`, or transcript browsing | [#1742](https://github.com/stacklok/mecatl/issues/1742) follow-on slices | Each surface retains independent interaction and ownership semantics. |

## Definition of done

1. Focused skills-panel, learned-lifecycle, bounded-control, and UI geometry tests pass, followed by `task lint`, `task test`, and `task test:race`.
2. `task docs` passes after updating the sole owning `/skills` behavior reference.
3. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green for the runtime change.
5. The implementation PR links this approved Plan / Interface PR and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Private field, helper, and test-fixture names remain implementation details. Delete or reuse the fixed scroll/window logic rather than retaining it as a competing physical-geometry authority.
- The plan intentionally replaces the fixed fourteen-line normal card policy. Golden updates must demonstrate the responsive normal card rather than accidentally pinning an arbitrary terminal height.
- The existing learned-skill section is a distinct selectable/lifecycle control within the same modal. Its visible chrome must be accounted for before calculating the external physical viewport; this does not authorize combining their state, identity, or action owners.
- Per-event wheel scrolling is compatibility behavior only. It must remain bounded to one external physical line and must not introduce a new input scheduler; [#1790](https://github.com/stacklok/mecatl/issues/1790) owns burst coalescing.
