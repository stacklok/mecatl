# Mecatui quieter conversation tool calls — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — a client-local presentation and keybinding change needs an observable UI contract but does not change server authority, persistence, or module boundaries.
**Decision record:** None — the existing typed scrollback, inspector, and keymap establish the necessary boundaries; this plan chooses how their current presentation is used, not a durable system architecture.
**Phase:** issue #1361, second slice: quiet settled calls and direct inspection
**Status:** proposed, 2026-10-04. Based on hands-on review after the landed `/toolcalls` inspector and the operator's 2026-10-04 decision to use `f9` for both reasoning and permanent-error detail.
**Delivery:** Split. Replacing the global detail gesture and changing conversation density deserve behavioral/interface review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1361](https://github.com/stacklok/mecatl/issues/1361).

The main conversation shows an active bordered tool card while a call is still in flight, then one quiet, borderless line for a settled top-level call, including an Edit, Write, Subagent, Team, or failed call. The one-line content mirrors the `/toolcalls` list: status glyph and readable label, tool name, and the same human-readable action/target intent. The list and conversation use **one UI-local semantic projection** of current scrollback, with only viewport, selection, and styling chrome allowed to differ. The existing inspector keeps complete received call arguments and results; no duplicate history is introduced. See [the landed inspector contract](mecatui-toolcalls-inspector.md) and [typed scrollback](../../cmd/mecatui/ui/internal/scrollback/scrollback.go).

`ctrl+t` opens `/toolcalls` from the main conversation rather than expanding every tool card. `f9` becomes a separate rebindable conversation-detail toggle for reasoning summaries and permanent non-tool error cards; it expands no tool results. This does not alter approval decisions or their context-local detail gesture. The owning public documentation for this user-facing change is [mecatui keybindings](../../user-docs/mecatui/keybindings.md); amend that page in the implementation PR, not this unshipped plan. Existing [TUI contributor guidance](../tui.md#conversation-and-feedback) and [logical conversation anchors](../adr/0301-logical-conversation-anchors.md) constrain layout, selection, and scrolling.

## Human decisions

- [x] Default density and statuses — Decision: show running tool calls as bordered cards; show settled successful and failed top-level calls as one borderless line, retaining the existing green/red glyphs plus non-color status words. A call is not claimed to have succeeded merely because it finished before the result was received.
- [x] Shared one-line language — Decision: conversation summaries use the same status, tool name, and sanitized intent projection as inspector list rows. Share the semantic projection instead of duplicating tool-specific intent switches; list cursor, geometry, and styling remain view-specific.
- [x] Detail gestures — Decision: in the main conversation, the configured `ctrl+t`-default shortcut opens the existing `/toolcalls` inspector; `f9` defaults to a separate rebindable, global expand/collapse action for reasoning summaries and permanent non-tool error cards only. Approval surfaces continue to own their existing configured `ctrl+t`-default shortcut and full-args/diff/plan views.
- [x] Existing keymap overrides — Decision: `Toolcalls` is the canonical remappable action (default `ctrl+t`), `ExpandConversation` is a new remappable action (default `f9`). Existing `ExpandTools` overrides are an accepted, deprecated alias for `Toolcalls` so their chord still opens inspection. Normalize the alias *within each configuration source* before the existing per-action precedence merge; old and new names in the same source conflict, while a higher-priority source overrides a lower-priority source. Neither name restores bulk tool expansion. Validate the effective shortcut against approval verdict chords because approval uses the same shortcut for details.
- [x] Edits and delegation — Decision: settled Edit/Write calls get the same single-line treatment; complete arguments remain in `/toolcalls`, and existing changed-files/approval surfaces remain separate. A live Subagent/Team card keeps its current bounded activity; detailed child activity remains in the F6 Agents view, not fabricated in the top-level inspector.
- [x] Permanent non-tool errors — Decision: `ExpandConversation` (`f9` by default) also reveals the existing full, terminal-sanitized received payload under the permanent-error card's summary, as the old global expansion did; when collapsed it remains a short summary with the **active `ExpandConversation` chord**, not a `ctrl+t` hint. Transient non-tool errors already display their text directly and remain unchanged. This is user-invoked, not an automatic expansion; the full received payload is not line-capped today and this plan adds no arbitrary truncation of diagnostic information.

## Interface contract

- **gRPC / protobuf:** None — consume the existing live tool, result, delegation, and reasoning events and session transcript; no new RPC or wire fields.
- **Exported Go APIs / interfaces:** None — only private mecatui packages and UI-local presentation types change; no engine or SDK exports.
- **Tool schemas:** None — `/toolcalls` remains a client command, not a model-facing tool.
- **CLI / config:** `keymap.Toolcalls` (default `ctrl+t`) is the main-conversation inspector-open binding; `keymap.ExpandConversation` (default `f9`) toggles only reasoning summaries and permanent non-tool error payloads in the main conversation. `keymap.ExpandTools` is a deprecated input alias for `Toolcalls`: normalize it per source (deprecated mecatl settings, mecatui settings, or CLI overrides) before merging those sources in their established priority order. Both names in one source are an error; a higher-priority source using either name overrides the lower-priority effective `Toolcalls` chord. Multiple chords per action remain supported, with the first advertised in short hints and the full set in help. The effective `Toolcalls` binding is checked against approval Allow/AllowAlways/Deny and other active-key collisions; both new actions obey global-key validation. Existing `--keymap Action=chord` and `keymap:` settings mechanisms work for both. Keep `/toolcalls` unchanged and available while idle or running. The approval modal keeps its existing context-local detail action using the effective `Toolcalls` chord(s), never opening the inspector; no other flag or settings key is added.
- **Events / persistence:** None — no new event or stored field; the canonical scrollback remains the sole source for both views. Reasoning summary reload is tracked separately in [#2079](https://github.com/stacklok/mecatl/issues/2079).
- **Security / authority:** None — the inspector and line render only the current session's received display-safe payload, with terminal controls sanitized and width/height bounded. The `f9`-default action opt-in exposes only the already received, terminal-sanitized permanent-error payload and reasoning text as today's expansion does; neither view fetches more data. No new tool execution, permission grant, child call inventory, cross-session read, or link dereference; approval focus and decision ownership remain unchanged.
- **Compatibility / migration:** Existing `ExpandTools` settings continue to load as an alias but their behavior intentionally changes from expand-all to open-inspector. Existing tool-card global expansion is retired; reasoning and permanent non-tool error cards share the new `ExpandConversation` toggle. `/toolcalls` list/detail and current transcript rehydration continue to work. Rebinds and contextual approval help must reflect actual chords; update the owning public keybindings/usage content in the implementation PR and run `task site:build`.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — read one tool call consistently in the conversation and inspector

Use the existing [inspector intent/status projection](../../cmd/mecatui/ui/toolcalls.go), [tool-card renderer](../../cmd/mecatui/ui/tool_block.go), and [typed scrollback snapshots](../../cmd/mecatui/ui/internal/scrollback/tool.go) rather than creating a second interpretation of tool arguments. Formatting after semantic projection may differ for the inspector cursor/gutter and the conversation width. Preserve stable block identity and renderer-owned cache/provenance [invariants](../tui.md#conversation-and-feedback) and [ADR 0301's logical reading-anchor boundary](../adr/0301-logical-conversation-anchors.md).

**Acceptance:**
- AC1.1: While a normal call is pending, it retains a bordered, bounded card with readable action/target and pending state. Once its canonical result is received, the same scrollback block becomes a single borderless line with `✓ done · <tool> · <intent>` (or `✗ failed · <tool> · <intent>` for errors); inspector list and conversation report the same semantic status, display name, and intent for that block before view-specific clipping, with only gutter/styling differences. MCP list/line labels agree on their human-readable display name while inspector detail still exposes the received full name.
  - verify: `TestMecatuiQuieterToolCalls_Scenario1_PendingAndSettledParity`
- AC1.2: Read, Grep, Shell, Edit/Write, an MCP tool, delegated Subagent/Team, unknown tool names, and malformed or hostile arguments receive a useful one-line summary without an invented target. Both surfaces agree on semantic status and intent, and narrow widths never print terminal controls or spill a second line; full received details remain reachable in `/toolcalls` even after settlement.
  - verify: `TestMecatuiQuieterToolCalls_Scenario1_SharedIntentAndSafety`
- AC1.3: The shared UI-local projection uses block identity, display name, untruncated sanitized intent, and explicit lifecycle state: pending and terminal-success-awaiting-result remain bordered and do not claim completion; a provisional result stays bordered and is labeled awaiting confirmation; canonical success/error becomes a `✓ done`/`✗ failed` line; terminal failure without a result becomes a `✗ failed` line without invented result text. Inspector and conversation report the same state; a later canonical result updates the existing block without duplication. Concurrent calls settle independently, and resumed completed/unresolved calls retain honest statuses without inventing lost results. Scrollback may expose its existing provisional flag to the UI-local projection without adding a server/API field.
  - verify: `TestMecatuiQuieterToolCalls_Scenario1_OutOfOrderAndResume`

### Scenario 2 — browse details without inflating the conversation

The local inspector is already available through `/toolcalls` during idle and running phases; keep its existing [list/detail navigation](mecatui-toolcalls-inspector.md#scenario-1---find-a-call-in-the-current-conversation) and [modal ownership](../tui.md#layout-and-navigation). The main shortcut is an alternative entry, not a conversation-wide expand mode; [ADR 0301](../adr/0301-logical-conversation-anchors.md) protects the hidden conversation's reading position.

**Acceptance:**
- AC2.1: Pressing the configured `Toolcalls` chord in the main conversation while idle or running opens the current-session inspector with the same empty-state, selection, and detail behavior as `/toolcalls`. It leaves the hidden conversation's reading position, prompt draft, and received stream intact; closing returns to the same context, including on narrow screens and `--no-mouse`.
  - verify: `TestMecatuiQuieterToolCalls_Scenario2_InspectorShortcutAndFocus`
- AC2.2: When a permission modal is active, the effective `Toolcalls` chord is instead the existing *approval-detail* action: ordinary asks open/close the full-args view, Edit/Write asks show the diff details, and plan asks keep their plan-detail behavior; none opens the inspector or changes a verdict. Other modal/help/overlay owners keep their keys. `ExpandConversation` is inert while approval owns the keyboard. Rebinding either action updates its relevant visible hint, rejects overlaps that would shadow Allow/AllowAlways/Deny, and never leaks a keypress into the prompt.
  - verify: `TestMecatuiQuieterToolCalls_Scenario2_ContextualShortcutAndOverrides`

### Scenario 3 — expand conversation details, not tool cards

Assistant reasoning and permanent-error cards are separate [conversation regions](../../cmd/mecatui/ui/render.go) from tool calls. Reasoning is explicitly a lossy summary, not authoritative chain of thought; keep its current caveat. The permanent-error card's [existing opt-in raw detail](../../cmd/mecatui/ui/internal/blocks/error_cards.go) is not in the inspector, so its affordance moves to `f9` rather than vanishing. The action is separately rebindable through the [keymap validator](../../cmd/mecatui/keymap/validator.go); follow the [TUI client boundary](../tui.md#client-boundary) and [AGENTS.md](../../AGENTS.md) effective-payload constraint.

**Acceptance:**
- AC3.1: `f9` (or the overridden `ExpandConversation` chord) toggles all available reasoning-summary stanzas between the existing collapsed header and expanded, caveated summary, including during streaming, *and* toggles permanent non-tool error cards between their existing short summary and full terminal-sanitized received payload. Collapsed hints use the configured `ExpandConversation` chord. This single opt-in action does not expand any tool result, Edit/Write diff, Subagent/Team trace, transient error, or approval modal; a turn with no summary remains unchanged.
  - verify: `TestMecatuiQuieterToolCalls_Scenario3_ConversationDetailToggle`
- AC3.2: Rebinding `Toolcalls` and `ExpandConversation` through either keymap source validates collisions, shows their effective chords in help and collapsed-reasoning/permanent-error hints, and never advertises the removed expand-all tool action. Legacy `ExpandTools` rebinds open the inspector at their existing chord; using old and new names in one source fails, while a higher-priority source overrides a lower-priority alias. The first active chord appears in compact hints and the full binding remains discoverable in help; collisions with approval verdict keys fail validation.
  - verify: `TestMecatuiQuieterToolCalls_Scenario3_RebindAndLegacyAlias`

### Scenario 4 — preserve long-run readability and non-tool diagnostics

Conversation frames are used for logical reading anchors and selection, not just pixels; changing row counts must preserve the [renderer provenance contract](../../cmd/mecatui/ui/rendered_frame.go) and [ADR 0301](../adr/0301-logical-conversation-anchors.md). Permanent provider-error detail remains available through the new conversation-detail action, without extending `/toolcalls` to non-tool errors.

**Acceptance:**
- AC4.1: At narrow and wide terminal widths, 100+ sequential settled calls take one line each rather than bordered multi-row cards, while a live call retains its bounded card and ordinary assistant/user/notice spacing remains readable. Scrolling back, selecting/copying text, resizing, and restoring auto-follow preserve the same logical block and semantic reading position despite cards shrinking during a run.
  - verify: `TestMecatuiQuieterToolCalls_Scenario4_LongRunAnchorsAndSelection`
- AC4.2: A permanent non-tool error still shows its brief summary by default and `f9` (or its active rebind) reveals its **complete received** terminal-sanitized payload with no new line cap, as current expansion does; pressing it again restores the short view. `ctrl+t` never routes that error to `/toolcalls`, and no error card advertises the wrong chord. Transient errors remain unchanged; both views fit width, preserve scrollback anchors and keep the expanded text reachable by scrolling, even for a long error.
  - verify: `TestMecatuiQuieterToolCalls_Scenario4_ErrorDetailAccess`
- AC4.3: The owning [public keybindings page](../../user-docs/mecatui/keybindings.md) and current TUI usage page describe `ctrl+t`, rebindable `f9`, one-line settled calls, approval precedence, and how to inspect details without presenting separately tracked summary reload as shipped.
  - verify: inspection — compare shipped interactions to the owning pages; run `task docs` and `task site:build`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Grouping consecutive settled calls into a single aggregate row | Later #1361 checkpoint | Try one-line-per-call first on short and 100+ call transcripts; grouping needs a separate reviewed interaction. |
| Click a conversation call to jump directly to its inspector detail | Later #1361 optional slice | Do not compromise native terminal selection, inline mode, or `--no-mouse`. |
| Render inspector results by content kind (diff, Markdown, Shell, JSON) | [#2066](https://github.com/stacklok/mecatl/issues/2066) | Do not block compact cards on a separate result-styling feature. |
| Persist/reload reasoning summaries | [#2079](https://github.com/stacklok/mecatl/issues/2079) | Preserve the currently received summary; this phase does not change storage. |
| UI-package structural refactor | [#2077](https://github.com/stacklok/mecatl/issues/2077) | Reuse the smallest semantic projection already in the UI; do not add a generic widget framework. |

## Definition of done

1. Focused offline tests prove scenario parity, key ownership, status ordering, hostile text, transcript rehydration, and rendered-frame/selection behavior. Review fixed-size narrow/wide examples and a long real run before accepting the result.
2. The implementation candidate passes `task lint`, `task test:race`, `task docs`, `task site:build`, `task ac-trace-strict`, and the offline demo's tool/ask/approval/result flow.
3. The implementation PR cites this merged Plan / Interface PR and baseline, updates the owning public pages, and passes `/panel-review` with no ship blockers. Only a human merges.

## Deferred decisions and known risks

- Inspector list cursor and bounded row width are distinct from the conversation frame; compare *semantic one-line projection* before applying view-specific clipping, rather than trying to share styled ANSI strings or copying results into an inspector-side store.
- Rendering must distinguish an available provisional result, an authoritative result, and a terminal lifecycle notice; the current compact metadata reports resolved/failed but does not itself certify a canonical result.
