# Authoritative mecatui session reload - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — the server already owns session truth; this change reconciles the terminal client's reconnect, startup-resume, and interactive-adoption projections without changing server authority, persistence, or public APIs.
**Decision record:** None — server-owned metadata and snapshot rehydration are established by [the mecatui client boundary](../tui.md#client-boundary); this plan changes only the client's reload policy and local ordering.
**Phase:** batch 1, authoritative session reconciliation before early title generation
**Status:** proposed, 2026-10-03. Requested by the directing operator for [issue #2071](https://github.com/stacklok/mecatl/issues/2071).
**Delivery:** Split. Several asynchronous session workflows and their ordering need an independent interface and behavior review.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#2071](https://github.com/stacklok/mecatl/issues/2071).

A completed mecatui session reload replaces server-derived facts with an authorized session snapshot. The terminal client retains view-only state such as draft text and scroll position. Reconnect, startup resume, and interactive adoption use this rule; ordinary footer/plan metadata refreshes must not undo a newer authoritative result. This is the prerequisite for [early live title generation, issue #2072](https://github.com/stacklok/mecatl/issues/2072); this plan does not start title work during a run.

The owning living guide is [Developing the mecatui terminal UI](../tui.md#client-boundary). Its current server-authority boundary already applies; user-facing instructions are unnecessary because reload requires no new operator action. The [existing title-generation plan](session-title-generation.md) provides the current revision and event contract. This plan changes the *client reload* rule only: a new snapshot can supersede an earlier live title revision, while live events still reject duplicates and older revisions within a connection.

## Human decisions

None — the operator has chosen server-authoritative reload, preservation of view state, and the batch order; this plan fixes their observable boundaries without introducing a new server contract.

## Interface contract

- **gRPC / protobuf:** None — use existing `GetSession`, `StreamSessionLive`, transcript retrieval, and their current message fields; no RPC, protobuf field, or server method changes.
- **Exported Go APIs / interfaces:** Add `TitleMetadataPresent bool`, `MainUsagePresent bool`, `ServerCapabilitiesPresent bool`, and `ResolvedModelPresent bool` to `cmd/mecatui/client.SessionSnapshot`; add `Usage client.Usage` and the same four presence fields to `cmd/mecatui/client.ResolvedModelMsg` (the field type is `Usage` inside package `client`). Populate them from the existing `GetSession` and compatibility response and carry them through refresh. No new methods or `engine/` exports; UI reload generation/fencing state remains package-private.
- **Tool schemas:** None — reload is terminal-client reconciliation, not a tool call.
- **CLI / config:** None — no new command, flag, setting, or default.
- **Events / persistence:** No new events, persisted fields, or event-log semantics. `session.title` remains revision-ordered within a live feed; a completed reconnect/resume/adoption snapshot replaces the displayed title, provenance, and revision even if its revision is lower. Subscribe to the replacement live feed before fetching the reconnect snapshot; do not expose feed events until the snapshot is installed. Replay buffered title events in their received order against the new snapshot revision (dropping duplicates/lower values). A current run result or other live update arriving during the snapshot fetch must not be rolled back by a stale response: if its cumulative usage/occupancy cannot be ordered against the snapshot, perform another bounded authoritative fetch after the run settles rather than adding a possibly duplicated increment. A bounded event-buffer overflow or another feed disconnect makes the reload incomplete and requires a fresh reconciliation. Other present session metadata replaces its client projection; absent legacy fields follow their specified fallback. Durable replay still owns pending approvals and transcript facts.
- **Security / authority:** `GetSession` and transcript/watch operations retain existing authorization and session-affinity requirements. Only a snapshot for the currently bound session may replace its metadata; failed or unauthorized reloads preserve the last confirmed display and show existing connection/error feedback. No optimistic UI action grants permission or replaces server-confirmed permission mode.
- **Compatibility / migration:** This supersedes only the snapshot-side higher-revision rule in [title-generation AC4.6](session-title-generation.md). Legacy missing fields do not erase known compatible values; when a server supplies a field, its value is canonical, including a lower title revision or context window. No stored-session migration or wire change. The title-generation contract otherwise remains unchanged until issue #2072 is approved.

## In scope - 3 scenarios, in implementation order

### Scenario 1 - A completed reconnect restores server session facts

After the live connection recovers, mecatui refreshes the bound session and adopts its title, provenance, revision, mode, session state, placement, resolved provider/model/effort/window, latest context occupancy, cumulative main usage, and advertised capabilities. The server response is authoritative even when a formerly displayed revision, context window, or usage total was higher. Extend the existing `GetSession` client projection so the reload message carries usage and presence information; the current `ResolvedModelMsg` omits usage and normally ignores occupancy. A nil title-metadata message is legacy absence (retain a last confirmed label if one exists), while a present title metadata message with an empty title clears the label. A nil resolved-model message, missing main usage bucket, or nil occupancy is unavailable rather than a confirmed zero; a present zero usage total or zero occupancy is confirmed. Distinguish absent server-wide capabilities from an explicitly present all-false capability response; preserve per-session media presence and its text-only state. See the [current refetch reducer](../../cmd/mecatui/ui/update.go), [snapshot projection](../../cmd/mecatui/client/session.go), and the [API snapshot boundary](../architecture/api-surface.md).

**Acceptance:**
- AC1.1: A successful reload replaces an in-memory title and revision with the server snapshot, even when the revision decreases or the authoritative title is empty; a later live `session.title` at a higher revision applies, while duplicate or lower live revisions do not.
  - verify: `TestMecatuiAuthoritativeReload_Scenario1_TitleRevertsAndLiveOrderingResumes`
- AC1.2: Reload adopts a same-ID resolved model's supplied provider, reasoning effort, and context window (including a smaller corrected window), confirmed mode/state/placement, and supplied cumulative usage and latest occupancy without conflating occupancy with lifetime usage.
  - verify: `TestMecatuiAuthoritativeReload_Scenario1_ReplacesSuppliedSessionFacts`
- AC1.3: An absent legacy title-metadata message retains a last known label without claiming a revision; a present empty title clears it. A missing resolved model remains unknown, a missing main usage bucket does not assert a zero total, and nil occupancy remains unknown; present zero usage/occupancy is adopted. A present all-false global capability response removes previously enabled controls, while an absent legacy capability response retains the established compatible fallback, including explicit session-media text-only presence.
  - verify: `TestMecatuiAuthoritativeReload_Scenario1_LegacyAndExplicitAbsence`

### Scenario 2 - Reload and live updates have an unambiguous order

A reconnect includes catch-up, an authorized snapshot, and a new live subscription. Server-derived data from an abandoned connection or obsolete refetch cannot overwrite the completed reload; a current live update cannot be erased by a snapshot fetched earlier. Other same-session refreshes (footer heal, plan handoff, or adoption follow-up) cannot silently undo a newer reload or confirmed live result. See the [reconnect reducer](../../cmd/mecatui/ui/update.go), [client live reader](../../cmd/mecatui/client/events.go), [revision tests](../../cmd/mecatui/ui/title_revision_test.go), and [API event boundary](../architecture/api-surface.md).

**Acceptance:**
- AC2.1: With the replacement live feed established first, a title event received during snapshot fetch survives if it is newer than that snapshot; an event already included by the snapshot is not reapplied. Stale events from a retired feed or session are ignored; a bounded buffer overflow triggers reconciliation instead of silently losing an update. No new steady-state inventory poll is introduced.
  - verify: `TestMecatuiAuthoritativeReload_Scenario2_SnapshotLiveRace`
- AC2.2: Out-of-order same-session snapshot responses and stale mode-change/rename completions cannot replace a newer server-confirmed state or clear a newer operator intent.
  - verify: `TestMecatuiAuthoritativeReload_Scenario2_StaleResponses`
- AC2.3: If the authoritative fetch fails, the client keeps its last confirmed state, shows that session metadata is not synchronized, and retries metadata fetch with the existing bounded reconnect backoff until success or session switch/exit; a second feed failure re-enters reconnection. No partial snapshot is adopted.
  - verify: `TestMecatuiAuthoritativeReload_Scenario2_FailedFetchPreservesState`

### Scenario 3 - Client view and run recovery survive a session reload

Reload updates session facts without replacing the user's unsent draft, queued prompts, navigation, modal/focus state, or conversation reading position. Recorded conversation and pending approvals still come from their respective transcript and durable watch/recovery paths rather than guesses derived from session metadata. On explicit session resume/adoption, the already fetched session snapshot wins over an older inventory row for title, state, placement, created time, and other overlapping fields; the row's inventory-only modified time remains a row value. See [session adoption](../../cmd/mecatui/ui/sessions.go), [startup resume](../../cmd/mecatui/ui/model.go), [conversation/view ownership](../tui.md#conversation-and-feedback), and the [domain session model](../architecture/domain-model.md).

**Acceptance:**
- AC3.1: Reconnecting or refreshing a bound session preserves unsent view state and does not duplicate or discard recorded conversation or pending approvals; its confirmed server permission mode remains visible.
  - verify: `TestMecatuiAuthoritativeReload_Scenario3_PreservesViewAndRecovery`
- AC3.2: Resuming or adopting a session displays its fetched snapshot's title, state, placement, usage, occupancy, and resolved model rather than stale overlapping fields from an inventory row; a later matching-session refresh remains canonical.
  - verify: `TestMecatuiAuthoritativeReload_Scenario3_AdoptionUsesSnapshot`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Earlier title generation, provisional live events, and crash/restart title policy | [issue #2072](https://github.com/stacklok/mecatl/issues/2072) | Batch 2 depends on this reload. |
| New transcript/event-log delivery or a generic client-state reset | Separate work | Existing durable replay and view-state ownership remain. |
| Changing server storage, permissions, or public wire fields | Separate contract | This plan reconciles existing authorized snapshots. |

## Definition of done

1. Focused offline mecatui client/UI proofs cover every acceptance criterion, including stale-message and live/reload interleavings.
2. `task lint`, `task test:race`, `task docs`, `task ac-trace-strict`, and the offline demo pass on the implementation candidate.
3. The implementation PR reports conformance to the approved plan and passes `/panel-review`; only a human merges it.

## Deferred decisions and known risks

- A `GetSession` response is a point-in-time snapshot; an implementation must fence stale in-flight responses and preserve newer live delivery. Ordering mechanisms are implementation detail, not license to silently discard a confirmed newer server update.
