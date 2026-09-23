# Session configuration generations - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural - this adds explicit replacement intent, two-stage durable exact-predecessor activation, an optional storage paging contract, additive protobuf fields, and a new default inventory projection.
**Decision record:** [ADR 0343](../adr/0343-session-configuration-generations.md)
**Phase:** session continuity and inventory clarity
**Status:** proposed, 2026-09-15. The operator resolved the intent, identity, activation, ambiguous-outcome, bounded-storage, compatibility, and scope decisions interactively.
**Delivery:** Split. Durable engine, snapshot, store, driver, transport, and inventory contracts need separate interface review before implementation.
**Expected tasks:** 4
**Issue:** [stacklok/mecatl#1490](https://github.com/stacklok/mecatl/issues/1490)
**Plan PR:** [stacklok/mecatl#1557](https://github.com/stacklok/mecatl/pull/1557)
**Approved baseline:** absent until the Plan / Interface PR merges

A model or reasoning-effort switch explicitly marks its successor as replacing the source. Default
session inventory hides a physical main session only after a retained, owner-visible main-session
target is server-ready and durably activates an exact predecessor edge. Ordinary forks remain
independent even when they change configuration, and clear or worktree-only successors remain
independent. Every physical session remains stored and directly addressable.

This plan adds one intent field, one optional exact-predecessor reference, and an optional
generation-aware pager. It does not create a Chat aggregate, durable root identity, generation
number, shared cross-session state, mutable head record, readiness field, or repair worker.

## Human decisions

None — the operator selected explicit replacement intent, predecessor-only exact references, two-stage fail-visible activation, exact-attempt resolution of ambiguous writes, bounded derived inventory, non-main pass-through, physical-session lifecycle semantics, presence-aware driver compatibility, and legacy physical-inventory fallback; explicit effort reset and client handoff recovery remain in their separately tracked issues.

## Interface contract

- **gRPC / protobuf:** `ForkSessionRequest` gains `bool replaces_source = 7`. Mecatui's model and effort switch requests set it; ordinary forks omit it or send false. Public `ListSessionsRequest` gains ordinary `bool include_replaced = 3`; absent and false both request the default head-only view from a generation-capable server. `SessionSummary` gains `bool is_replaced = 19`. HTTP `POST /v1/sessions/{source}/fork` accepts the equivalent `replaces_source` JSON field, and `GET /v1/sessions` accepts the equivalent `include_replaced` query parameter. Driver `SaveRequest` gains `string incarnation = 4`, `string predecessor_session_id = 5`, and `string predecessor_incarnation = 6`, allowing an opaque-snapshot driver to index the saved row's exact identity and optional predecessor. Driver `PageSessionMetadataRequest` gains **presence-aware** `optional bool include_replaced = 6`: absent means legacy physical inventory, present false means head-only, and present true means all physical rows. A new server always sends this field explicitly when calling a driver that advertises `generation_inventory`. `SessionMetadataEntry` gains only `bool is_replaced = 30` for this feature; `SessionStoreCapabilitiesResponse` gains `bool generation_inventory = 7`. The paging response exposes no root or predecessor graph fields. No RPC is added or removed.
- **Exported Go APIs / interfaces:** Add `session.SessionPredecessorRef{ID SessionID, Incarnation IncarnationID}`. The reference is exact identity metadata, not a credential or capability. `Session` stores an optional predecessor privately and gains `Predecessor() *SessionPredecessorRef`, `SetPredecessor(source *Session) error`, and `RestorePredecessor(*SessionPredecessorRef)`. `SetPredecessor` is the one-time activation transition: it accepts only an owner-matching valid main-session source whenever the valid main-session target has no predecessor, including after the target's initial predecessor-free persistence; it derives the reference from `source.ID` and `source.Incarnation()` and rejects replacement or removal after attachment. It never accepts caller-supplied reference fields and no longer requires a pristine, never-saved target. `RestorePredecessor` is restricted to trusted snapshot/storage rehydration and treats nil, zero, or structurally malformed values as no edge so transcript restore remains available. Existing exported `Session` fields remain unchanged. Add `port.SessionGenerationMetadataPageRequest{SessionMetadataPageRequest; IncludeReplaced bool}`, `port.SessionGenerationDiscoveryMeta{SessionDiscoveryMeta; IsReplaced bool}`, and `port.SessionGenerationMetadataPage{Sessions []SessionGenerationDiscoveryMeta; NextCursor *SessionMetadataCursor; TotalCount int}`. Add the optional `port.SessionGenerationMetadataPager`, which embeds `SessionMetadataPager` and declares `SupportsSessionGenerationInventory() bool` plus `PageSessionGenerationMetadata(context.Context, SessionGenerationMetadataPageRequest) (SessionGenerationMetadataPage, error)`. The result exposes no self reference, predecessor, root, or graph. Existing `SessionMetadataPager`, `SessionMetadataPageRequest`, `SessionMetadataPage`, and `SessionDiscoveryMeta` remain unchanged.
- **Tool schemas:** None - replacement grouping is not model-facing and changes no tool.
- **CLI / config:** None - no flag, setting, precedence rule, or key binding is added. Mecatui `/sessions` consumes the server's default inventory and adds no historical view. Its model and effort switch paths set `replaces_source=true`; its ordinary session-fork path leaves the field false.
- **Events / persistence:** `sessnap` persists only optional predecessor ID and incarnation fields and restores malformed or partial values as no edge without blocking transcript load. JSONL, memstore, Redis, and capable remote drivers round-trip the saved row's self incarnation and optional exact predecessor in private projections. A replacement request uses two durable writes and the edge itself as the only activation marker: (1) create and persist the target with no predecessor, so source and target are both visible and independently addressable; (2) complete required server-side target registration, broker, reattachment, and equivalent readiness work; then (3) attach the exact predecessor once and persist the target snapshot and store projection atomically. Only the final persistence activates replacement visibility. Existing preparation that already precedes first persistence may remain there, but the general contract does not rely on that ordering. A crash or failure before activation leaves both rows visible. A failure after proven activation, including response delivery, client handoff, or source close, leaves the server-ready target as head and does not roll the edge back. `Create`/`Save` errors are potentially ambiguous: after an initial or activation write error, the server probes the exact target ID and incarnation and continues only if the authoritative loaded row matches that exact identity and the expected predecessor state (absent initially, exact source on activation). Missing, colliding, or mismatched state fails closed. If storage is unreadable, the attempt stops without another mutation; once storage is readable, the authoritative predecessor state determines whether both rows remain visible or the ready target is the head. The attempt must not blindly mint another target. Cross-request retry idempotency is not added. No event type, durable root, `SessionGeneration`, generation number, replacement-reason enum, readiness field, eager migration, backfill, or reconciliation worker is added.
- **Storage projection / paging:** A generation-capable store maintains a private exact-predecessor and reverse-edge projection plus derived visibility. `Save`, `Delete`, and generation-relevant metadata mutations update snapshot metadata, forward/reverse edges, derived `is_replaced` membership, ordering/ownership indexes, and the existing pager mutation generation atomically, or fail visible. Adding, changing through restore, or removing an edge recomputes the affected undirected component sufficiently to detect cycles; a cycle, inability to derive, or corrupt component marks every affected retained row unreplaced rather than hiding one. Malformed, dangling, incarnation-mismatched, cross-owner, and cross-kind edges hide no row. After owner filtering, grouping applies only to valid main-session rows; every scheduled, delegated, debug, unknown, malformed-kind, or future owner-visible non-main row passes through unchanged and unreplaced. Generation-aware `Page` reads the already-derived bounded projection and must not scan or load every snapshot per page. Scan/reference adapters may derive that projection from their bounded metadata index, never transcripts; indexed adapters choose their own data structure. Remote drivers own the same boundedness and atomic/fail-visible guarantee behind `generation_inventory` negotiation. Filtering and derived visibility precede total count, ordering, cursor construction, and page slicing. The selected `IncludeReplaced` view is bound into cursor scope.
- **Cursor invalidation:** Every `Save`, `Delete`, or metadata mutation capable of changing inventory membership, ordering, ownership, predecessor-edge validity, component validity, or derived `is_replaced` advances the pager's existing mutation generation in the same atomic update. Every outstanding cursor from the prior generation then returns `ErrSessionMetadataCursorRestart`; pagers never continue across a possibly changed projection. Cursor scope also binds `include_replaced`, so changing the requested view returns the same restart error.
- **Security / authority:** Inventory remains owner-filtered and content-free. A predecessor reference is not authenticated by knowledge of its ID/incarnation pair and grants no ownership, continuation, rename, delete, placement, or transcript authority. Public transports accept only `source_session_id` plus replacement intent; they never accept a predecessor incarnation. Both transport paths use the existing `ForkSessionSuccessor` chokepoint, which owner-authorizes the source, reloads and reauthorizes it under the source lock, and derives the predecessor from that loaded aggregate. The target inherits the authorized source owner, and `SetPredecessor` independently rejects non-main or owner-mismatched aggregates. A retained successor can hide only an owner-visible valid main session whose exact ID and incarnation it references after server readiness and atomic activation. Malformed, dangling, incarnation-mismatched, cross-owner, and cross-kind edges hide no row. Cyclic, corrupt, or underivable connected components fail visible. Existing source authorization, source lock/lease, target preparation, and action revalidation remain authoritative. When ownership enforcement is disabled, generation metadata preserves that existing single-tenant compatibility posture and does not claim independent authentication.
- **Compatibility / migration:** Compatibility is mandatory at the wire/schema level, while the new server's default public result intentionally changes to head-only when generation inventory is supported. All protobuf and snapshot fields are additive. Old snapshots and malformed predecessor values load with no edge; old clients ignore `is_replaced`; old servers ignore `replaces_source` and public `include_replaced`. An old public client against a new generation-capable server omits public `include_replaced` and therefore intentionally receives head-only inventory; this is the feature's intended additive behavioral change, not byte-identical result preservation. An old mecatui also omits replacement intent, so its own configuration-carrying forks remain independent. A new client against an old server completes the existing switch but gets physical inventory because intent and view fields are ignored. The existing engine pager structs and interface remain byte-for-byte unchanged. `session_generation_inventory` is advertised only when the active store implements `SessionGenerationMetadataPager` and reports support, or when a remote driver advertises its separate `generation_inventory` capability. With only the legacy pager, both public request values return identical physical pages, totals, and cursors; with neither pager, listing retains `ErrSessionMetadataPagingUnsupported`. New engine identifiers are Added/minor and require `task api:update` plus an `engine/CHANGELOG.md` entry. Rewriting a new snapshot through an older binary can discard unknown predecessor fields; a later new binary exposes that row independently.

### Compatibility matrix

| Pairing | Paging selector sent to storage | Inventory result |
|---|---|---|
| Old public client -> new generation-capable server | Public field absent selects head-only; when storage is remote, the server sends the driver field explicitly false | Head-only; `is_replaced` is ignored by the old client. This intentional behavior change is the feature. |
| New public client -> old server | New public fields are ignored | Legacy physical inventory; replacement intent is not recorded. |
| New server -> old/non-capable driver | No generation capability, so no generation paging call | Legacy physical inventory and no public generation capability. |
| Old server -> new driver | Driver `include_replaced` is absent | Legacy physical inventory, preserving the old server's expectations. |
| New server -> new generation-capable driver | Driver field is always present: false for heads, true for all rows | Requested derived view with `is_replaced`; cursors are scoped to the selector. |

## In scope - 4 scenarios, in implementation order

### Scenario 1 - explicit replacement intent activates exact durable identity

Replacement identity uses the existing incarnation boundary from
[ADR 0258](../adr/0258-cryptographic-session-incarnations.md), separate from the trusted
producer taxonomy in [ADR 0217](../adr/0217-session-discovery-continuation.md).

**Acceptance:**
- AC1.1: `ForkSessionRequest.replaces_source=true` first persists a distinct target with no predecessor. Mecatui's model and effort switch paths set the field. The result is the same when resolved configuration equals the source and when the request also carries a worktree selector.
  - verify: `TestADR_0343_ExplicitReplacementIntent`
- AC1.2: An ordinary fork with `replaces_source` omitted or false has no predecessor even when provider, model, reasoning effort, title, or worktree selector differs. Clear and worktree-only successors also have no predecessor.
  - verify: `TestSessionConfigurationGenerations_Scenario1_IndependentSuccessorsIgnoreOverrides`
- AC1.3: After initial target persistence, required server-side preparation succeeds before the existing `ForkSessionSuccessor` path reloads and reauthorizes the source under its lock. `SetPredecessor(source)` rejects non-main or owner-mismatched aggregates, derives the exact ID/incarnation from that authorized source rather than wire input, and attaches it once. Atomic persistence of that edge and the derived store projection is the sole activation boundary. A crash or failure before activation leaves both rows visible and directly addressable; a failure after proven activation leaves the prepared target as head.
  - verify: `TestSessionConfigurationGenerations_Scenario1_TwoStageActivationIsFailVisible`
- AC1.4: Snapshot and local and remote store conformance proofs round-trip only the optional exact predecessor. Driver Save supplies self incarnation on both writes, no predecessor on initial persistence, and exact predecessor on activation. Legacy, partial, and malformed values become no edge without preventing transcript load.
  - verify: `TestSessionConfigurationGenerations_Scenario1_PredecessorMetadataRoundTrips`

### Scenario 2 - default inventory uses a bounded derived head projection

The generation-aware pager owns grouping because the existing inventory contract requires
filtering before pagination and counting, as described in
[ADR 0217](../adr/0217-session-discovery-continuation.md).

**Acceptance:**
- AC2.1: For retained activated main sessions `A -> B -> C`, default inventory returns C and `include_replaced=true` returns all three physical sessions with A and B marked replaced. A predecessor-free prepared target does not hide its source. No durable root, readiness field, or generation number participates.
  - verify: `TestSessionConfigurationGenerations_Scenario2_PredecessorChainAndHistoricalView`
- AC2.2: Sibling successors `A -> B` and `A -> C` hide A while B and C remain visible heads. Deleting C leaves B as a head and A hidden; deleting both successors reveals A. For `A -> B -> C`, deleting B makes A and C visible because C's dangling edge is ignored. Deletion removes only the selected physical session and its existing sidecars.
  - verify: `TestSessionConfigurationGenerations_Scenario2_SiblingsAndPhysicalDeletion`
- AC2.3: Save/Delete atomically maintain the private exact-predecessor/reverse-edge projection, derived visibility, metadata indexes, and pager generation. Edge addition/removal recomputes the affected undirected component; malformed, dangling, incarnation-mismatched, cross-owner, cross-kind, cyclic, corrupt, or underivable components fail visible without invalidating unrelated components.
  - verify: `TestSessionConfigurationGenerations_Scenario2_AtomicProjectionAndCorruptionFailVisible`
- AC2.4: Generation-aware pages read the already-derived bounded metadata projection without loading or scanning every snapshot or any transcript per page. Grouping applies only after ownership filtering to valid main rows; all owner-visible non-main or unrecognized rows pass through unreplaced. Any membership, ordering, ownership, edge-validity, component-validity, or `is_replaced` mutation invalidates old cursors, and cursor scope rejects a different `include_replaced` view.
  - verify: `TestSessionConfigurationGenerations_Scenario2_BoundedProjectionAndCursorInvalidation`

### Scenario 3 - capability negotiation preserves mixed-version meaning

Generation-aware paging is an optional extension of the compatibility model in
[ADR 0248](../adr/0248-sdk-compatibility-and-error-contract.md).

**Acceptance:**
- AC3.1: The public server advertises `session_generation_inventory` only when the active store implements `SessionGenerationMetadataPager` and reports support; a remote store reports support only when its driver advertises the separate `generation_inventory` capability.
  - verify: `TestSessionConfigurationGenerations_Scenario3_FeatureMatchesPagerCapability`
- AC3.2: A generation-aware pager honors both public inventory views and scopes cursors to the selected view. A store with only the unchanged `SessionMetadataPager` advertises no generation feature and returns identical physical pages, counts, and cursors for both request values. A store with neither pager retains `ErrSessionMetadataPagingUnsupported`.
  - verify: `TestSessionConfigurationGenerations_Scenario3_LegacyPagerFallback`
- AC3.3: The full compatibility matrix is proved: new client -> old server yields physical inventory; old public client -> new capable server intentionally yields head-only inventory while old replacement requests remain independent; new server -> old driver yields physical inventory and no feature; old server -> new driver omits the driver selector and receives physical inventory. Transcript loading, exact-ID access, and existing actions remain available in every pairing.
  - verify: `TestSessionConfigurationGenerations_Scenario3_MixedVersionCompatibilityMatrix`
- AC3.4: A new server always sends generation-capable driver paging `optional include_replaced` explicitly: false for head-only and true for all physical rows. A generation-aware driver receives self incarnation and optional predecessor on Save and returns `is_replaced` without exposing root or graph fields. Absent driver selector means legacy physical inventory.
  - verify: `TestSessionConfigurationGenerations_Scenario3_DriverSelectorPresenceAndProjection`

### Scenario 4 - publication ambiguity and historical access stay server-authoritative

Mecatui retains the server-authoritative inventory and transcript boundaries from
[ADR 0217](../adr/0217-session-discovery-continuation.md).

**Acceptance:**
- AC4.1: After an ambiguous initial persistence error, the server probes the exact target ID/incarnation and continues preparation only when the row matches and has no predecessor. Missing or mismatched/colliding state fails closed, leaves the source visible, and never blindly creates another target for that attempt.
  - verify: `TestSessionConfigurationGenerations_Scenario4_AmbiguousInitialPersistIsProbed`
- AC4.2: After an ambiguous activation persistence error, the server probes the same exact target. A matching exact predecessor proves activation and permits target handoff; a proven absent predecessor leaves source and target visible; mismatch or a missing target row fails closed. Unreadable storage stops the attempt without another mutation; after recovery, the authoritative predecessor state determines visibility. No generation projection hides the source unless it contains the committed exact edge for a server-ready target.
  - verify: `TestSessionConfigurationGenerations_Scenario4_AmbiguousActivationIsFailVisible`
- AC4.3: Against a generation-aware server, mecatui `/sessions` uses the head-only public default; gRPC and HTTP `include_replaced=true` return the same physical rows and `is_replaced` values. Exact-ID transcript loading reaches hidden predecessors and predecessor-free prepared targets, while rename, delete, fork, lease, event-log, placement, and recovery remain physical-session scoped.
  - verify: `TestSessionConfigurationGenerations_Scenario4_HistoricalAPIParityAndExactIDAccess`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Explicit reasoning-effort `auto` versus omitted inheritance | [#1485](https://github.com/stacklok/mecatl/issues/1485) | Presence-aware effort reset is independent of inventory grouping. |
| Effort-switch hydration and source-close failure | [#1486](https://github.com/stacklok/mecatl/issues/1486) | This plan orders required target readiness before activation but does not redesign each preparation step or source close. |
| Conditional chat-head mutation or cross-request retry idempotency | evidence-driven follow-up | Sibling activated successors remain visible heads; this plan resolves ambiguous writes only within the exact target attempt. |
| Chat aggregate, `ChatID`, durable root, `SessionGeneration`, readiness field, shared title, aggregated usage, or family-wide actions | separate product decision | The predecessor edge is both the only relationship metadata and the activation marker. |
| Mecatui historical-generations view | later interaction-design work | The API exposes `include_replaced`; mecatui changes only its default result. |
| Chain repair, reconciliation, eager migration, or backfill | operational evidence | Invalid or legacy predecessor metadata and underivable components fail visible. |
| Chain-aware retention or cascade deletion | separate retention decision | Existing physical-session lifecycle remains authoritative. |
| Broker, lease, rehydration, registration, response-delivery, client-handoff, and source-close redesign | [#1486](https://github.com/stacklok/mecatl/issues/1486) or separate reliability work | Required readiness must precede activation; the internals and handoff recovery remain separate. |
| Backend-specific graph/index data structure | adapter implementation | The atomic, bounded, reverse-edge, component-recompute, and fail-visible behavior is contractual; representation is private. |

## Definition of done

1. `task generate`, `task api:update`, `task lint`, `task test`, `task api:check`, `task docs`, and `task site:build` pass.
2. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. The owning user documentation and living architecture describe explicit replacement intent, two-stage predecessor activation, bounded predecessor-derived head inventory, non-main pass-through, the administrative historical view, and mixed-version fallback.
5. The implementation PR links the Plan / Interface PR and approved commit and reports engine API, snapshot, local-store, driver, transport, ownership, bounded pagination, activation, ambiguous-outcome, cursor-invalidation, and compatibility conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Concurrent explicit replacements and retries after the exact attempt is abandoned can create sibling heads. Inventory reveals every retained head and does not choose a winner.
- Physical deletion or retention can break an edge and reveal older sessions. Inventory reads never mutate or repair metadata.
- A failure after proven activation can leave a ready target that the initiating client did not receive. Inventory and exact-ID access make the durable target recoverable; automatic handoff recovery is separate.
- A predecessor-free target left by failed preparation remains an independent visible session. This is the deliberate fail-visible cost of not hiding a usable source too early.
- Deployments backed by an older external store or driver keep physical-session inventory until that backend implements the optional bounded pager.
- Indexed stores pay component-recompute work on generation metadata mutations so page reads remain bounded; unusually large connected components can make writes more expensive.
