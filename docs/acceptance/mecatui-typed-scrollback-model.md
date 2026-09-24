# Mecatui typed scrollback model — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — extracts a client-local typed scrollback state model and preserves established rendering, cache, and navigation behavior without changing public APIs, persistence, authority, or deployment behavior.
**Decision record:** None — the package boundary and its migration rationale are internal to `cmd/mecatui/ui`; no durable architectural decision is introduced.
**Phase:** scrollback-model follow-up to functional conversation-card rendering
**Status:** landed, 2026-09-23. Implementation candidate on `impl/mecatui-typed-scrollback-model`; authoritative when its implementation PR merges. Plan / Interface PR [#1818](https://github.com/stacklok/mecatl/pull/1818) merged at `0b447fcd23d191e076c1ba94695b1d33b4326c1b`.
**Delivery:** Split. The new UI-internal state-model contract, typed transition boundary, and preserved scrollback invariants need Plan / Interface review before implementation.
**Expected tasks:** deferred to orchestration after the Plan / Interface review.
**Issue:** [stacklok/mecatl#1789](https://github.com/stacklok/mecatl/issues/1789).

Mecatui will replace the broad mutable scrollback `block` union with a private `ui/internal/scrollback` logical-card model. The model owns its ordered cards, stable UI-local identity, typed payload transitions, and content revision. The root `ui` package remains the adapter from client events to typed model operations and remains the renderer/cache/frame/viewport owner. The existing `ui/internal/blocks` package remains a pure presentation compiler; it receives renderer-owned immutable presentation inputs rather than conversation state.

This plan follows the functional-card boundary and preserves ADR 0301's ownership split: scrollback state owns creation and mutation; the renderer owns cache storage, layout, and frame provenance; the conversation-view controller owns reading position. It intentionally treats a card's content revision as an observable logical-state version used by the renderer's cache, rather than a mutation convention distributed among UI callers ([ADR 0301](../adr/0301-logical-conversation-anchors.md#5-isolate-the-conversation-view-controller-inside-the-tui), [functional-card plan](mecatui-functional-conversation-card-rendering.md)).

## Human decisions

None — the operator selected a private `ui/internal/scrollback` package; a sealed, closed payload family; typed mutations issued by `ui`; immutable snapshots for consumers; distinct ordinary-tool, Subagent, and Team card payloads; and rendering outside the scrollback package. The interface sketch below is intentionally non-binding on Go identifiers and may be refined during implementation only if all stated ownership and behavioral contracts remain true.

## Interface contract

- **gRPC / protobuf:** None — the root UI continues to project existing `cmd/mecatui/client` events; no wire message, field, service, or client protocol changes.
- **Exported Go APIs / interfaces:** None — no public module API changes. The internal package provides a package-private-to-mecatui contract: a mutable conversation model, stable block identity and revision, a closed payload/snapshot family, and typed transition operations. Exact Go package, type, field, and method names are non-binding. Consumers can identify a snapshot's card kind and use an exhaustive type switch, but cannot mutate a card, revision, or payload directly or add a new payload variant outside the package.
- **Tool schemas:** None — tool calls, results, artifacts, expansion behavior, and their client-visible meanings are unchanged.
- **CLI / config:** None — no flags, settings, theme policy, or keybindings change.
- **Events / persistence:** None — the root UI maps the existing live and replay client events onto the typed internal transitions. Block identities, revisions, model indexes, snapshots, render cache entries, frames, anchors, and selection remain process-local and are neither persisted nor added to an event payload.
- **Security / authority:** None — no input source, permission, secret, or authority boundary changes. Existing terminal sanitization for plain cards, the separate Glamour Markdown path, and bounded client-only delegation previews remain mandatory; the model must not create a parent-conversation content path ([ADR 0079](../adr/0079-delegation-observability-convergence.md#decision)).
- **Compatibility / migration:** Client-internal source migration. Existing event ordering, ordinary tool resolution, assistant streaming, card content, cache fast paths, frame provenance, anchor restoration, selection behavior, and Subagent/Team live-state behavior remain compatible. Direct internal `block{...}` test fixtures migrate to focused card fixtures or narrowly scoped model test helpers.

## Non-binding interface sketch

The implementation may rename or reshape these identifiers, but it must preserve the stated mutation, discovery, and ownership rules:

```go
// ui/internal/scrollback

type Block struct {
    // private stable ID, logical revision, and sealed mutable payload
}

type BlockSnapshot struct {
    ID       BlockID
    Revision uint64
    Payload  PayloadSnapshot
}

type PayloadSnapshot interface {
    Kind() Kind
    payloadSnapshot() // sealed: only scrollback defines card variants
}

// Examples of distinct variants: UserCardSnapshot, AssistantCardSnapshot,
// ToolCardSnapshot, SubagentCardSnapshot, TeamCardSnapshot, NoticeCardSnapshot,
// HookCardSnapshot, ErrorCardSnapshot, DeliveryCardSnapshot, and TurnStatCardSnapshot.

type Conversation struct { /* private ordered cards, appendix, and lifecycle indexes */ }

func (c *Conversation) Len() int
func (c *Conversation) SnapshotAt(index int) BlockSnapshot
func (c *Conversation) AppendixSnapshot() (AppendixSnapshot, bool)
func (c *Conversation) AddToolCall(ToolCall) BlockID
func (c *Conversation) ResolveTool(callID string, result ToolResult) bool
func (c *Conversation) StartSubagent(callID string, start SubagentStart) bool
func (c *Conversation) UpdateSubagent(callID string, update SubagentUpdate) bool
func (c *Conversation) UpdateTeam(callID string, update TeamUpdate) bool
```

`Conversation` owns state transitions and advances the matched enclosing card revision exactly once for each change observable through a scrollback snapshot, including card and Agents-overlay state. Its read surface supports ordered snapshots plus the separate appendix snapshot; snapshots and transition inputs detach mutable slices/maps so callers cannot mutate model-owned state. A Subagent or Team lifecycle may specialize a pending ordinary tool card into its matching card payload, but retains its stable block identity, ordered position, and the shared call/result/artifact facts through every supported event ordering. `ui` translates client events into these typed inputs; the renderer adapts snapshots into Markdown or `blocks` presentation inputs and owns styling, terminal width, expansion, caching, frame provenance, anchors, selection, and viewport behavior. `Kind()` is a derived closed-family discriminator; renderer switches must deliberately handle every package-defined variant and have a regression test when a new variant is added.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — sealed typed scrollback state and transitions

The logical model must eliminate fields that are meaningful only for another card family while preserving the current append order, UI-local IDs, delayed result lookup, and streaming behavior. Current direct post-append mutation gateways must become package-owned typed transitions so a caller cannot bypass revision advancement (`cmd/mecatui/ui/conversation.go` (`currentAssistant`, `resolveTool`, `subagentBlock`, `teamBlock`)). This retains ADR 0301's requirement that UI-local block identity belongs to the conversation rather than the protocol or persisted state ([ADR 0301](../adr/0301-logical-conversation-anchors.md#1-make-a-logical-conversation-anchor-authoritative)).

**Acceptance:**
- AC1.1: `ui/internal/scrollback` owns the ordered scrollback cards and the changed-files appendix's document-local lifecycle, assigns each appended card and first-observed appendix one stable UI-local identity, and exposes immutable snapshots carrying that identity, the logical revision, and one sealed typed payload variant.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario1_StableIdentityAndImmutableSnapshots`
- AC1.2: User, assistant, ordinary tool, Subagent, Team, notice, turn-stat, error, hook, and delivery cards have distinct payload state. Ordinary tool, Subagent, and Team cards may share a small call/result value only for their genuine common lifecycle facts; no payload carries unrelated optional card-family state.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario1_PayloadFamiliesAreSpecialized`
- AC1.3: Typed append, streamed-assistant, tool-resolution, Subagent, and Team transitions perform the matching state update and advance the enclosing card revision exactly once when and only when the state visible through any scrollback snapshot consumer changes, including main-card and Agents-overlay task/finding state; idempotent replay of an unchanged update does not advance it.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario1_TransitionsAdvanceVisibleRevision`
- AC1.4: Unknown, terminal, or mismatched lifecycle updates fail without creating a card, changing an unrelated card, changing appendix state, or advancing a revision.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario1_InvalidTransitionsAreNoOps`
- AC1.5: First observation of a changed file allocates the appendix identity once; it contributes provenance only while expanded and non-empty, and collapse restores the reader to the preceding scrollback block according to ADR 0301.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario1_ChangedFilesAppendixIdentityAndFallback`
- AC1.6: Reconstructing or clearing a conversation resets scrollback cards, appendix identity/membership, model indexes, and renderer-associated cache/frame/anchor/selection state so a new document cannot reuse another document's identity or rendered state.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario1_RebuildResetsDocumentIdentity`

### Scenario 2 — event projection and specialized card behavior

The root UI remains the projection adapter: it turns existing client live and replay events into typed scrollback transitions, without passing raw client event objects into the model. Ordinary tool results still resolve by tool-call identity even when they arrive after later cards; delegation start may specialize the corresponding pending tool card while retaining its block identity. Subagent and Team cards can then evolve separately without optional state on ordinary tool cards. Fleet and Parallel group state remain UI-owned side projections, not scrollback payloads. Bounded delegation previews remain client-only and must not enter the parent conversation ([ADR 0079](../adr/0079-delegation-observability-convergence.md#decision)).

**Acceptance:**
- AC2.1: Existing live and replay event-reduction paths construct equivalent typed card state for user input, assistant/reasoning streaming, notices, stats, hooks, errors, deliveries, tool calls, and tool results; existing ordering and displayed content remain unchanged.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario2_LiveAndReplayProjectionParity`
- AC2.2: The interleavings `tool.call → Subagent/Team start and updates → tool.result`, `tool.call → tool.result → background Subagent end`, and a late non-tail result each resolve only their matching ordinary or specialized card. They retain its stable block ID, position, call arguments, result/error state, and typed artifacts, and invalidate the required render cache path through the updated revision.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario2_ToolDelegationLifecycleInterleavings`
- AC2.3: Subagent and Team lifecycle events specialize and update only their corresponding card variants, preserving bounded traces, usage, terminal state, team lanes/tasks/findings, and client-only delegation isolation. Subagent, Team, and UI-owned Parallel projections retain their existing control-byte scrubbing and bounded-preview caps; a raw child-content canary never enters the parent conversation or a parent tool-result state. Neither `subagentFleet` nor `parallelGroups` becomes a scrollback payload in this change.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario2_DelegationCardsRemainSpecialized`, `TestMecatuiTypedScrollbackModel_Scenario2_DelegationPreviewIsolation`
- AC2.4: Production UI code cannot directly mutate payload fields or revisions; direct union fixtures migrate to focused card constructors/snapshots, except narrowly scoped test helpers that deliberately prove revision/cache invalidation behavior.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario2_ProductionUsesTypedTransitions`
- AC2.5: Mutating caller-owned transition input or a nested slice/map in a returned snapshot cannot mutate model state; card media, artifacts, traces, lanes, task dependencies, and findings retain their distinct model-owned values.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario2_TransitionsAndSnapshotsOwnNestedData`

### Scenario 3 — renderer, cache, and reader-position continuity

The typed model is logical state only. It owns no styling, terminal geometry, wrapping, prepared lines, cache storage, frame provenance, viewport, anchors, selection, Bubble Tea model, or client event type. The renderer retains the existing revision × presentation-context cache contract, while `ui/internal/blocks` stays a pure preparation boundary ([functional-card plan](mecatui-functional-conversation-card-rendering.md#scenario-3--renderer-owned-cache-frame-and-reader-position-continuity)). ADR 0301 requires block identity and semantic row provenance to remain sufficient for deterministic anchor restoration without a second transcript ([ADR 0301](../adr/0301-logical-conversation-anchors.md#2-render-line-provenance-with-the-existing-frame)).

**Acceptance:**
- AC3.1: Renderer adapters discover each snapshot's card kind through its sealed snapshot contract, dispatch card-specific rendering deliberately, and pass only renderer-owned immutable presentation inputs to Markdown or `ui/internal/blocks`; scrollback imports neither rendering nor client-event dependencies.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario3_ScrollbackBoundaryIsLogicalOnly`
- AC3.2: A settled card with unchanged logical revision and presentation context reuses cached output without fresh model snapshot/render preparation; streamed tail and non-tail updates invalidate no more cache/prefix state than the existing revision contract requires.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario3_CacheRevisionFastPathPreserved`
- AC3.3: Rendered lines and frame provenance remain lockstep, and a width reflow, card specialization, late result, or expansion change preserves the ADR 0301 logical reading position and selection survival/clearing rules.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario3_FrameAnchorAndSelectionContinuity`
- AC3.4: The model/snapshot/cache migration adds no second O(rendered-bytes) transcript copy and preserves current depth-scaling and cache-equivalence proofs.
  - verify: `TestMecatuiTypedScrollbackModel_Scenario3_ScrollbackRetentionAndDepthContract`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Visual redesign of individual tool, Subagent, or Team cards | Focused card UX work | This plan enables isolated experimentation but preserves current rendered behavior. |
| `subagentFleet` and `parallelGroups` representation | Separate projection-model work | They are UI-owned aggregation views, not ordered main-scrollback cards. |
| Modal/surface UI and other overlays | Surface-specific work | Their geometry and lifecycle are distinct from the main conversation's block/frame/cache path. |
| gRPC, HTTP, client protocol, engine, or persistence changes | Future product work if required | This is a client-local projection and runtime-model refactor. |
| Viewport virtualization, transcript eviction, on-demand history, generic public component framework | Profiling-led or demonstrated-need follow-up | ADR 0301 preserves the full transcript and prohibits smuggling a retention redesign into this refactor. |

## Definition of done

1. `task lint`, `task test`, `task api:check`, and `task docs` pass.
2. `go run ./cmd/mecademo` remains green; no engine or runtime behavior changes.
3. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
4. The implementation PR links the approved Plan / Interface PR and commit and reports conformance with every interface-contract category.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Exact internal Go identifiers, constructors, indexing data structures, and file layout are intentionally non-binding; implementation may refine them without widening the package's ownership or behavioral contract.
- The sealed snapshot family must permit deliberate renderer dispatch while preventing consumer-defined variants or consumer-side mutation; a broad behavior interface is explicitly out of scope.
- A specialization transition must preserve block ID and order so cache invalidation, frame provenance, anchors, and selection do not observe a replacement document block.
- Snapshot slices/maps must not expose mutable model-owned backing storage. Tests must prove snapshot ownership for card media, artifacts, traces, lanes, tasks, and findings.
