# Mecatui mention palette bounded list — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — the existing inline mention menu adopts the established package-private bounded-list interaction contract, changing candidate selection, physical-row geometry, and phase-specific key ownership without changing a public API, persistence boundary, protocol, or system architecture.
**Decision record:** None — this is a focused application of the landed Mecatui bounded-control contract; its surface-local behavior belongs in this plan rather than a durable architecture record.
**Phase:** bounded-list adoption, mention palette slice
**Status:** in-progress, 2026-09-23. Implementation is underway against approved Plan / Interface PR [#1828](https://github.com/stacklok/mecatl/pull/1828).
**Delivery:** Split. The mention palette has distinct local filesystem discovery, path completion, literal-prose fallback, and streaming interaction behavior that need their interaction contract reviewed before implementation.
**Expected tasks:** 1
**Issue:** [stacklok/mecatl#1742](https://github.com/stacklok/mecatl/issues/1742).

This #1742 slice migrates only the inline `@` mention palette to the reusable `bounded.List` control. The menu remains an input-reactive dropdown above the prompt: normal typing continues to edit the prompt, candidate discovery remains local and bounded, and selecting a row only rewrites the active mention token. Attachment resolution, upload, and the literal-prose fallback remain unchanged.

The existing menu maintains a numeric cursor and projects at most eight logical candidates through `scrollWindow` in [`cmd/mecatui/ui/mention.go`](../../cmd/mecatui/ui/mention.go). The replacement follows the pointer-owned, stable-ID, ANSI-aware physical-row contract established by [Mecatui bounded scroll and cursor control](mecatui-bounded-scroll-selection.md) and [`cmd/mecatui/ui/internal/bounded`](../../cmd/mecatui/ui/internal/bounded). It does not extend that package or introduce a widget framework.

## Human decisions

- [x] Candidate window — Decision: retain the existing 4,000-entry bounded filesystem walk and hidden-directory pruning, but collect at most 64 sorted matching file paths. The physical list viewport, rather than an eight-result candidate cap, bounds what is visible. Operators narrow a result set by typing more of the path.
- [x] Fitting and card geometry — Decision: use `bounded.Clip`, with a palette outer width of `min(128, terminal width)`. Derive the physical-body capacity from the rows remaining after preserving the prompt and footer, capped at eight physical rows including up to two overflow indicators. The card shrinks to its visible rows when the result set is smaller. When no candidate-content row fits, suppress the complete card.
- [x] Standard presentation — Decision: retain the `files` card header, `@` path prefix, and surface-owned hint/indicator wording. Render every selectable path through `presentListRow` with the standard one-cell selection gutter and unbordered selected-row style. Filesystem-derived path text is terminal-sanitized before rendering without altering the raw displayed spelling used for identity and completion. No mouse interaction is added.
- [x] Navigation and keys — Decision: while the visible menu owns input, Up/Down move one logical path. `ScrollU`/`ScrollD` first expose the remaining physical segments of an oversized selected path, then select the first path beginning after the current physical window; reverse paging is symmetric. Tab or Enter completes the selected path; Escape dismisses the menu. Home and End remain unclaimed, matching the slash-command palette. When the card is suppressed, the mention menu claims none of these keys.
- [x] Streaming ownership — Decision: the visible mention menu has the same menu-key ownership while idle and while a run streams. In particular, Enter completes the selected path and Escape dismisses the menu while it is visible; after dismissal, ordinary running Enter and Escape handling resumes, including enqueue/steer and cancellation.
- [x] Refresh and identity — Decision: paths use their displayed spelling, including `~/`, `./`, and `../` prefixes, as stable list IDs. Filtering, rescans, and resize preserve both the selected path and the top visible `{path ID, physical line}` anchor when they remain. When either disappears, use the bounded control's clamped replacement without later snap-back.

## Interface contract

- **gRPC / protobuf:** None — the menu remains entirely in the proto-free mecatui renderer and uses no RPC or message changes.
- **Exported Go APIs / interfaces:** None — the work consumes the existing package-private `cmd/mecatui/ui/internal/bounded.List`; it adds no exported Go symbol or interface.
- **Tool schemas:** None — no model-facing tool name, input, output, or instruction changes.
- **CLI / config:** None — no flag, setting, key-binding name, or default changes. Existing remappable `ScrollU` and `ScrollD` page the visible mention palette; Home, End, and mouse remain unclaimed.
- **Events / persistence:** None — candidate paths, list anchors, cursor selection, and geometry remain ephemeral client state.
- **Security / authority:** None — candidate discovery retains the present local client-process workspace/home resolution, bounded walk, hidden-directory pruning, and regular-file attachment gate. Every filesystem-derived path remains terminal-sanitized before rendering, while raw displayed spelling remains the completion and selection value. This slice does not expand filesystem authority, server workspace access, upload behavior, permission, trust, or secret handling.
- **Compatibility / migration:** The mention menu replaces its numeric cursor and fixed `scrollWindow` projection with a pointer-owned `bounded.List`. It preserves token recognition, slash-palette mutual exclusion, literal-prose fallback for unresolved/non-regular paths, the 4,000-entry walk bound, `~/` and leading traversal spelling/resolution, and attachment revalidation. It raises only the matching candidate cap from eight to 64, uses stable displayed-path IDs, and renders standard selectable rows through `presentListRow`. The slash palette, attachment processing, conversation viewport, and every other #1742 surface remain unchanged.

## In scope — 1 scenario, in implementation order

### Scenario 1 — mention completion presents bounded, stable selectable paths

When a single-line non-command prompt ends in an `@` token, Mecatui discovers up to 64 matching local file paths through its existing bounded walk and presents them as a physical-row-bounded `bounded.List`. The list owns only stable selection, anchors, clipping, and physical geometry. The mention surface keeps token detection, filesystem discovery, path insertion, card chrome, candidate text, overflow wording, and phase-specific routing. This follows the pointer-owned-control and row-presentation decisions in [the landed bounded-control plan](mecatui-bounded-scroll-selection.md#human-decisions), the client-only UI boundary in [`docs/tui.md`](../tui.md), and the repository invariants in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC1.1: A usable mention palette has outer width `min(128, terminal width)`, no line wider than its offered display-cell width, and a body derived from remaining frame height that contains at most eight physical rows including up to two overflow indicators. A smaller result set produces no blank body rows. Degenerate list geometry suppresses the complete card without panic or input displacement.
  - verify: `TestMecatuiMentionBoundedList_Scenario1_GeometryAndIndicators`
- AC1.2: Candidate discovery retains the current 4,000-entry walk bound and hidden-directory pruning, returns sorted matching regular-file paths, and admits at most 64 candidates. A user can reach every admitted path through the visible list; narrowing the token filters that bounded set without changing attachment eligibility.
  - verify: `TestMecatuiMentionBoundedList_Scenario1_BoundedDiscoveryAndReachability`
- AC1.3: Every visible candidate is terminal-sanitized, clipped to the available content width, and rendered through `presentListRow` with an equal-width unselected gutter. Up/Down move one logical path and minimally reveal it. `ScrollU` and `ScrollD` first expose remaining selected-path segments, then select the next/preceding path at the physical-window boundary; accurate above/below indicators expose every admitted candidate. Home, End, and mouse input remain unclaimed. Sanitizing a hostile pathname neither emits its terminal controls nor changes the raw pathname completed into the prompt.
  - verify: `TestMecatuiMentionBoundedList_Scenario1_SelectionPresentationAndPaging`, `TestMecatuiMentionBoundedList_Scenario1_SanitizesFilesystemPaths`
- AC1.4: Re-filtering, rescanning, and resizing preserve the selected displayed path and the top visible `{path ID, physical line}` anchor when they remain. If either path disappears, the control adopts the clamped replacement and does not restore the removed selection or anchor after the old path reappears.
  - verify: `TestMecatuiMentionBoundedList_Scenario1_StableSelectionAndAnchorsAcrossRefresh`
- AC1.5: While the card is visible, idle and running phases both reserve Up, Down, `ScrollU`, `ScrollD`, Tab, Enter, and Escape for mention navigation, paging, completion, and dismissal. In particular, running Enter completes instead of steering/enqueueing and running Escape dismisses instead of cancelling. After the card is dismissed or suppressed, the ordinary phase/prompt handlers receive those keys unchanged.
  - verify: `TestMecatuiMentionBoundedList_Scenario1_PhaseSpecificKeyOwnership`
- AC1.6: Completion preserves the existing text and attachment contract: it rewrites only the trailing mention token, retains `~/`, `./`, and `../` spelling, leaves unresolved or non-regular `@` tokens as literal prose, and does not change later regular-file revalidation or client-local upload behavior.
  - verify: `TestMecatuiMentionBoundedList_Scenario1_PreservesCompletionAndAttachmentContract`
- AC1.7: The owning Mecatui guidance in `user-docs/mecatui/using-the-tui.md` and `user-docs/mecatui/keybindings.md` states that a visible mention selection consumes Enter and Escape before ordinary running-phase behavior, while remaining concise and omitting live clipping, overflow, and card-presentation details.
  - verify: inspection — verify the two named owning pages describe the changed key precedence and pass `task docs` and `task site:build`.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Slash-command palette | Landed #1742 slice | This plan aligns only where the operator selected shared width, paging, and unclaimed-key behavior. |
| Attachment resolution, attachment content, capability gates, and upload | Existing mention contract | Preserve the existing local regular-file gate and literal-prose fallback. |
| Main conversation viewport, text selection, and streaming-tail behavior | Existing conversation ownership work | The mention list is an inline input surface, not conversation browsing. |
| Home, End, mouse, click, hover, or pointer activation | Later explicitly planned interaction work | Preserve slash-palette parity and keyboard-only activation. |
| Smooth-wheel burst coalescing | #1790 | Do not combine raw-wheel performance work with this migration. |
| Other selectable inventories and detail panes | Later #1742 slices | Classify each independently as `bounded.List`, `bounded.Viewport`, or retained simple behavior. |

## Definition of done

1. Focused mention, UI, and bounded-control tests, `task lint`, `task test`, and `task test:race` pass.
2. `task docs` and `task site:build` pass after updating the owning Mecatui guidance if its durable behavior wording changes.
3. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green for the runtime change.
5. The implementation PR links this approved Plan / Interface PR and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Exact private field layout and helper names remain implementation details. The list must be pointer-owned so Bubble Tea value-model copies retain one control state.
- `bounded.Clip` deliberately bounds the displayed path while the complete displayed path remains the selection ID and completion value. The menu must not derive identity from an index or clipped rendering.
- Keep card borders, headers, hints, overflow wording, sanitization, filesystem walking, and completion/attachment activation local. The bounded package remains theme-, filesystem-, and Bubble Tea-free.
- The current direct `@` syntax can select files outside the client workspace through existing leading traversal or literal-home rules. This slice preserves that explicitly documented client-local sharing boundary; it is not authority to broaden it.
