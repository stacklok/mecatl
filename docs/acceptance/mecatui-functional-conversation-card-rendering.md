# Mecatui functional conversation-card rendering — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — restructures the client-local conversation presentation pipeline and its cache contract without changing public APIs, persistence, authority, or deployment behavior.
**Decision record:** None — the mecatui-internal rendering boundary and its migration rationale are local to `cmd/mecatui/ui`.
**Phase:** conversation-card rendering convergence after the landed card-layout correction
**Status:** landed, 2026-09-21. Implementation candidate; authoritative when its implementation PR merges. Plan / Interface PR [#1739](https://github.com/stacklok/mecatl/pull/1739) merged at `183bd78f02a0021c1eecb22368cf257e26950249`; the operator authorized the targeted permanent-error replay parity amendment on this implementation branch.
**Delivery:** Split. The internal intra-UI API, card-family migration boundary, deterministic cache-key contract, and preserved scrollback invariants require interface review before implementation.
**Expected tasks:** deferred to orchestration after the Plan / Interface review.

Mecatui will compile each structured, non-Markdown main-conversation presentation from an immutable input snapshot and explicit layout into a deterministic prepared result. The result contains final bounded lines and lockstep semantic row metadata; it has no cache, transcript, viewport, or Bubble Tea state. The current renderer remains the owner of block identity, cache storage and lifetime, frame provenance, prefix reuse, selection, and logical-anchor restoration.

This is a follow-up to the landed card-layout work. That work established raw-content-before-decoration width handling; it did not establish one functional rendering contract across the main conversation, leaving follow-on fixes spread across `cmd/mecatui/ui`. The client-only scope and the need to preserve the existing frame/anchor/cache contract follow [ADR 0301](../adr/0301-logical-conversation-anchors.md) and the repository's UI-layering rules in [AGENTS.md](../../AGENTS.md).

## Human decisions

None — the operator selected the main-conversation scope, excluded modal/surface UI, selected a stateless functional compiler, and selected a single 32-byte digest of canonical render inputs as the reusable cache key.

## Interface contract

- **gRPC / protobuf:** None — this is a client-local projection over existing `cmd/mecatui/client` messages; no wire message, field, or service changes.
- **Exported Go APIs / interfaces:** None — the change adds no public Go API. Its UI-internal package boundary exposes only the behavior needed by the root renderer: immutable per-family input snapshots; explicit render layout/appearance snapshots; final card-local decorated lines with an equal-length structural-row slice; and one opaque fixed-size cache identity. A structural row carries the semantic region, text-bearing status, canonical visible-text source offset, derived-row fallback, semantic leading column, and grapheme span. The root renderer alone attaches document-local block ID, conversation indentation, block kind, and inter-block separators without parsing decorated output. Exact package, type, field, and function names are deliberately non-contractual.
- **Tool schemas:** None — tool calls, results, artifacts, and Ctrl+t expansion retain their existing schemas and client-visible meaning.
- **CLI / config:** None — no flags, settings, theme selection policy, or keybindings change; existing Ctrl+t expansion remains the control for complete tool output.
- **Events / persistence:** None — conversation block IDs, card inputs, prepared output, cache entries, and keys are process-local UI state; no session snapshot, event log, or restart contract changes.
- **Security / authority:** None — existing plain-text terminal sanitization and the separate Glamour Markdown path remain mandatory; this introduces no new input source, permission, secret, or authority path.
- **Compatibility / migration:** Client-internal, source-compatible migration. Existing rendering output, wrapping semantics, card expansion, selection, logical-anchor restoration, and cache fast paths remain compatible except that padding-derived double wraps/blank rows are corrected and persisted permanent provider failures replay with the same permanent-error presentation as their live result.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — deterministic prepared conversation-card contract

The presentation boundary must be functional rather than receive mutable `block`, `renderer`, or `Model` state. This preserves ADR 0301's separation: the conversation owns block mutation, the renderer owns layout and caching, and the conversation-view controller owns reading position ([ADR 0301](../adr/0301-logical-conversation-anchors.md#5-isolate-the-conversation-view-controller-inside-the-tui)).

**Acceptance:**
- AC1.1: A UI-internal conversation-card package defines immutable per-family input values, an explicit layout containing every render-relevant geometry, appearance/frame, hint, and rendering-dialect snapshot, and prepared output containing final card-local decorated lines plus an equal-length structural-row slice. The row slice carries every field needed to construct a `renderedRow` without parsing styled output or retaining semantic source text.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario1_PreparedRowsCarryStructuralProvenance`
- AC1.2: Card preparation and cache-identity derivation from an input snapshot and layout are deterministic and neither retain nor observe subsequent mutation of caller-owned input, nested slices/maps, callbacks, conversation, renderer, viewport, terminal, clock, or process environment state.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario1_PrepareAndKeyOwnImmutableSnapshots`
- AC1.3: Each card family derives one opaque 32-byte cache identity from a canonical explicit encoding of every render-relevant input and layout value, including width, expansion, visible hints, concrete appearance/frame data, and rendering-format version. A changed render-relevant value produces a different identity and re-prepared output; unchanged snapshots are byte-identical. Digest equality is the accepted identity for this local, non-persistent, non-adversarial in-memory cache.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario1_CacheKeyTracksRenderInputs`
- AC1.4: The card-preparation package owns neither cache storage/lifetime nor transcript/block identity, selection, viewport, or Bubble Tea state; it imports only the mecatui UI/theme/client dependency closure and no engine, host `internal`, or protobuf package.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario1_CardsPackageDependencyBoundary`

### Scenario 2 — one functional path for structured conversation cards

The main conversation's structured non-Markdown presentations migrate to the shared prepared-output contract, but retain per-family immutable inputs and `Prepare…` functions rather than a generic mutable block/union input. Tool cards and their result, artifact, diff, and delegation variants migrate first. Notice, hook, error, delivery, turn-stat, and user-prompt presentations then migrate through their own preparation functions, preserving their distinct label, semantic transformation, and terminal-safety rules. This remains within the client-only UI boundary in [AGENTS.md](../../AGENTS.md): UI code must not import the engine, host adapters, or protobuf directly. Assistant Markdown and its reasoning presentation retain the dedicated Glamour path, while the input rail retains its intentional fixed-background behavior, as preserved by the landed [card-layout plan](mecatui-card-layout.md).

**Acceptance:**
- AC2.1: Tool cards, including `Read`, render raw dynamic rows within the derived body width before styles, borders, and padding are applied; no styled outer-card reflow produces an additional wrapped row or padding-only row.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario2_ReadCardWrapsExactlyOnce`
- AC2.2: Tool-card headers, arguments, results, typed artifacts, Edit/Write diffs, and Subagent/Team/Parallel projections preserve their current collapsed/expanded content and fit their final outer-card width at zero, tiny frameless-fallback, narrow, normal, and capped layouts, including unbreakable text.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario2_ToolVariantsPreserveWidthAndExpansion`
- AC2.3: Notice, hook, permanent/non-permanent error, delivery, turn-stat, and user-prompt presentations preserve their existing labels, prefixes, collapsed/error transformations, recognized-fence handling, media placeholders, terminal-safety treatment, and width behavior through per-family preparation functions; no generic structured-block input or mode flags replace those rules. A persisted `ResultMsg` with `Permanent == true` replays as the same permanent-error presentation as its live result, including the collapsed summary and expanded raw-payload behavior.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario2_PerFamilyPreparedBlocksPreserveSemantics`, `TestMecatuiFunctionalConversationCards_Scenario2_PermanentErrorReplayMatchesLive`
- AC2.4: Representative plain structured inputs containing C0/C1/ESC/DEL/control-format bytes render no unsafe terminal sequence while retaining permitted layout newlines/tabs; the Glamour Markdown path is not passed through this plain-text policy.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario2_PlainCardsRemainTerminalSafe`
- AC2.5: Assistant Markdown, reasoning-summary rendering, and the input rail remain outside the cards package and retain their existing Glamour, emoji-width, and intentional fixed-background behavior.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario2_MarkdownAndInputExceptionsRemainSeparate`

### Scenario 3 — renderer-owned cache, frame, and reader-position continuity

The existing renderer and rendered frame remain the adapters between prepared cards and the viewport. ADR 0301 requires frame lines and row provenance to remain lockstep, retains cached settled blocks and prefix reuse, and prohibits introducing a second full transcript or a virtualization redesign ([ADR 0301](../adr/0301-logical-conversation-anchors.md#2-render-line-provenance-with-the-existing-frame)).

**Acceptance:**
- AC3.1: `block.rev` remains the renderer-owned cheap generation/admission guard. Only on a changed generation or cache miss does the renderer snapshot a card input, derive its canonical 32-byte cache identity, and prepare output; the stored identity is the prepared-output identity rather than a second set of parallel cache dimensions.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario3_RevisionGuardAvoidsSettledCardRehash`
- AC3.2: The renderer adapts `Prepared.Rows` atomically into rendered-frame provenance without parsing decorated output or retaining a second semantic-text copy; every cached frame has line/provenance lockstep.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario3_FrameProvenanceMatchesPreparedRows`
- AC3.3: Selection in a prepared tool argument or result retains its exact visible text and endpoint context after an unrelated append and width reflow; it clears when the selected source changes or collapse hides the selected row. Expand/collapse, a late tool result, and terminal-width changes preserve the ADR 0301 logical reading position.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario3_AnchorsAndSelectionSurviveCardReflow`
- AC3.4: At substantial scrollback depth, a streamed tail update prepares/renders only the invalidated tail; a non-tail mutation invalidates the required prefix boundary without rebuilding settled blocks; and an unchanged spinner/input frame neither prepares cards nor replaces viewport content.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario3_IncrementalCacheFastPath`
- AC3.5: Prepared/cache/frame state retains no second O(rendered-bytes) transcript copy and preserves the existing cache-equivalence and depth-scaling proofs for the normal collapsed-card, inactive-selection path.
  - verify: `TestMecatuiFunctionalConversationCards_Scenario3_ScrollbackPerformanceContract`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Modal and surface UI, including approval, Agents, Models, Sessions, MCP, and other overlays | Separate surface-specific work | These use the `surface` lifecycle and geometry contract rather than the main conversation's block cache, frame provenance, and logical-anchor pipeline. |
| gRPC, HTTP, client-message, engine, or persistence changes | Future work only if a product contract requires one | This remains a mecatui-local refactor and rendering correction. |
| Viewport virtualization, transcript eviction, or on-demand history | Profiling-led follow-up | ADR 0301 explicitly preserves the full-transcript viewport and forbids smuggling a retention redesign into a rendering refactor. |
| A cross-command or public UI-component framework | Future demonstrated need | The implementation remains restricted to the mecatui UI tree and must not become a repository-wide abstraction. |

## Definition of done

1. `task lint`, `task test`, `task api:check`, and `task docs` pass.
2. `go run ./cmd/mecademo` remains green; no runtime or engine behavior changes.
3. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
4. The implementation PR reports that no gRPC/protobuf, exported API, tool-schema, CLI/config, event/persistence, or security/authority interface drift occurred.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- A `CacheKey` digest is an in-memory UI cache discriminator, not a persisted or adversarial trust boundary. Canonical encoding must be explicit rather than reflective; a newly render-visible input requires a key regression proof.
- Tool-card frame geometry and visual hints are render inputs, not hidden renderer state. Their immutable snapshots must reach both preparation and canonical-key construction.
- Existing tool-card provenance currently derives semantic rows while `preparedToolCard` is live. The migrated result must preserve that atomic line/row relationship rather than reconstruct semantics by parsing framed Lip Gloss output.
- The previous card-layout plan remains a historical acceptance record. Its README status is corrected from draft to landed in this planning change; it is not rewritten to claim this follow-up abstraction.
