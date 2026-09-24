# Mecatui full-width tool cards — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this changes a deliberate, user-visible mecatui layout contract for every main-conversation tool execution, but it does not change protocols, APIs, durable state, authority, or subsystem ownership.
**Decision record:** None — the behavior is confined to the existing client-local renderer and reuses its established terminal-size, wrapping, and resize paths, so no durable architecture decision is introduced.
**Phase:** mecatui conversation layout
**Status:** proposed, 2026-09-24. Plan/documentation layer of the explicitly authorized simultaneous two-PR stack; implementation remains in the child branch.
**Delivery:** Split. The directing human explicitly waived the normal merge-before-implementation sequencing and requested simultaneous stacked Plan / Interface and Implementation PRs.
**Expected tasks:** 1
**Spine waiver:** On 2026-09-24, after the sequencing conflict was explained, the directing human explicitly confirmed `i-would-like-to-detect-the-siz` as the documentation/plan PR and `impl/mecatui-full-width-tool-cards` as its implementation PR.
**Issue:** None — direct operator request.
**Plan PR:** [#1865](https://github.com/stacklok/mecatl/pull/1865).
**Approved baseline:** Waived only for this explicitly authorized simultaneous stack; human merge authority and the implementation gates remain unchanged.

Main-conversation tool cards, including the `Skill` tool used to activate skills and read their assets, will occupy the same terminal-width budget as ordinary conversation text. The existing Bubble Tea resize path remains authoritative: cards reflow when the terminal width changes, retain their normal frame whenever the established card frame fits, fall back to the existing terminal-safe frameless rendering at tiny widths, and never overflow the terminal.

## Human decisions

None — the operator explicitly chose full-width framed execution cards, matching the width available to conversation text; no configurable or alternate width policy remains to decide.

## Interface contract

- **gRPC / protobuf:** None — tool-call and tool-result events, messages, fields, and transport behavior remain unchanged.
- **Exported Go APIs / interfaces:** None — only package-private mecatui layout behavior and tests change.
- **Tool schemas:** None — `Skill` and every other tool retain their existing names, arguments, results, and model-visible descriptions.
- **CLI / config:** None — full-width tool cards become the unconditional mecatui behavior; no flag, setting, or precedence rule is added.
- **Events / persistence:** None — no event, conversation, snapshot, cache-key, or persisted-state representation changes.
- **Security / authority:** None — terminal sanitization, permission handling, secret scrubbing, trust, and tool-execution authority remain unchanged.
- **Compatibility / migration:** This is an intentional visual behavior change for mecatui. Existing sessions need no migration; their retained tool blocks re-render at the current terminal width when displayed or resized.

## In scope — 1 scenario, in implementation order

### Scenario 1 — framed tool execution uses the conversation width

A terminal user follows a tool execution in the main conversation. Mecatui already receives the live terminal dimensions and reflows the transcript through its resize reducer; the tool-card renderer uses that same width-bearing path within the client boundary described by the [architecture overview](../architecture.md) and the [mecatui guide](../tui.md). The existing card-layout contract continues to wrap raw dynamic content before styling and framing, as established by the landed [mecatui card-layout plan](mecatui-card-layout.md), but the final frame no longer applies the 100-column readability cap or the additional two-column right inset. The card begins at the existing conversation-block indent and its right edge reaches the terminal's final column, matching the horizontal budget used by conversation text. This remains a minimal client-local change under the dependency and user-facing documentation rules in [AGENTS.md](../../AGENTS.md).

Because skill activation is the ordinary `Skill` tool, it follows this same rendering path rather than gaining a separate skill-specific card. Collapsed previews, `ctrl+t` expansion, selection, terminal sanitization, and display-row caps retain their existing behavior; only the offered horizontal width changes. Width-driven viewport replacement also retains the conditional selection and logical reading-anchor rules established by [ADR 0301](../adr/0301-logical-conversation-anchors.md): identical selected text and endpoint context may survive reflow, changed or hidden selected content clears safely, manually scrolled readers keep their semantic position, and tail-follow remains at the conversation tail.

**Acceptance:**
- AC1.1: On a terminal wider than the former 100-column cap, every main-conversation tool card renders its outer frame at the renderer's complete conversation-content width, so the block indent plus the framed card exactly fits the terminal width with no additional right inset.
  - verify: `TestMecatuiFullWidthToolCards_Scenario1_WideToolCardFillsConversationWidth`
- AC1.2: A `Skill` call and a representative ordinary tool call use the same full-width card layout in both collapsed and expanded states; complete arguments and results remain available through `ctrl+t` without a tool-name-specific rendering exception.
  - verify: `TestMecatuiFullWidthToolCards_Scenario1_SkillAndOrdinaryToolsShareLayout`
- AC1.3: Resizing an existing transcript from wide to narrow and back reflows the framed tool card to the current conversation width and never emits a row wider than the terminal. The normal frame remains at widths that can contain its chrome and a content column; smaller widths retain the established terminal-safe frameless fallback.
  - verify: `TestMecatuiFullWidthToolCards_Scenario1_ResizeReflowsWithoutOverflow`
- AC1.4: Width-driven tool-card reflow preserves expansion state and follows ADR 0301: selection survives only when its endpoint context and selected visible text remain identical, otherwise it clears; a manually scrolled reader retains a logical reading anchor, while a tail-following reader remains at the tail and continues following subsequent events.
  - verify: `TestADR_0301_full_width_tool_card_reflow_preserves_logical_view_state`
- AC1.5: Collapsed result-row limits, expanded complete content, raw-before-style wrapping, terminal-control sanitization, and semantic card-row provenance remain unchanged while using the larger body width.
  - verify: `TestMecatuiFullWidthToolCards_Scenario1_ContentAndSafetyContractsRemainIntact`
- AC1.6: The owning public TUI guide tells users that framed tool cards use the available conversation width and that `ctrl+t` changes detail visibility rather than card width.
  - verify: inspection — `user-docs/mecatui/using-the-tui.md` owns the terminal user's tool-card workflow and wording.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Centred inventory and detail overlays such as `/skills`, `/agents`, Models, or permission cards | Separate overlay-specific design | These are modal reading and selection surfaces, not main-conversation tool execution cards. |
| A dedicated full-height or alternate-screen tool-result view | Separate interaction proposal | This change fills horizontal conversation width only and preserves transcript scrolling. |
| Configurable maximum card widths or user-selectable layout modes | Separate configuration proposal | The operator requested one consistent full-width behavior; no new policy surface is needed. |
| Engine, provider, tool-result, or event changes | Separate protocol proposal if needed | Existing events already carry all content required by the client renderer. |

## Definition of done

1. Focused mecatui renderer and resize tests pass, followed by `task lint`, `task test:race`, `task docs`, `task site:build`, and `task api:check` on the final candidate.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green and demonstrates tool execution, permission ask/approval, and result delivery.
4. The owning public mecatui guide describes the shipped full-width card behavior without duplicating the rendering implementation.
5. The stacked implementation PR links this Plan / Interface PR, reports interface conformance, and proposes the plan's `landed` transition.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Wider cards reduce wrapping and therefore change collapsed result display-row accounting: more source content may fit within the existing row cap. Tests must assert the cap remains defined in visual rows rather than pinning obsolete line breaks.
- Full-width borders make wide structured output easier to scan horizontally but create longer prose measures. This is the explicit product choice; implementation must not retain a hidden readability cap or introduce a second width policy.
