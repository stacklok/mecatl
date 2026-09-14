# Draft-aware session inventory — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — this adds exported engine and port contracts, durable discovery metadata, public and driver protobuf fields, and automatic continuation behavior.
**Decision record:** [ADR 0334](../adr/0334-draft-aware-session-inventory.md)
**Phase:** session continuity and inventory clarity
**Status:** proposed, 2026-09-14. The operator selected a locally grouped Drafts tab; this proposed contract awaits Plan / Interface review.
**Delivery:** Split. The change crosses engine, durable stores, remote driver, server, and mecatui boundaries, so separate interface review is valuable.
**Expected tasks:** 3
**Issue:** [#1458](https://github.com/stacklok/mecatl/issues/1458)
**Plan PR:** [#1459](https://github.com/stacklok/mecatl/pull/1459)
**Approved baseline:** absent until the Plan / Interface PR merges

A valid main session created before its first genuine user prompt is a durable draft, not a
corrupt or disposable session. Mecatui keeps drafts discoverable and explicitly continuable,
but separates them from ordinary Chats and never picks them automatically for latest resume.
The source-free distinction must come from one persisted-history classifier, never a title,
turn count, lifecycle state, or ID heuristic.

This plan supersedes the automatic-latest portion of landed
[Session continuity UX](session-continuity-ux.md) AC6.3. Its authoritative transcript,
exact-resume, stale-running, ownership, and first-prompt run-entry guarantees remain unchanged.

## Human decisions

None — the operator selected a separate locally grouped Drafts tab for known-empty main sessions, and [ADR 0334](../adr/0334-draft-aware-session-inventory.md) resolves the resulting durable metadata and compatibility boundary.

## Interface contract

- **gRPC / protobuf:** `SessionSummary` gains open-string `activity_state = 18`. Driver `SessionMetadataEntry` gains open-string `activity_state = 29`; driver `SaveRequest` gains `string activity_state = 3`, used for both Save and Create so an opaque-snapshot driver receives the harness-computed scalar without decoding a snapshot. Driver `SessionStoreCapabilitiesResponse` gains `bool activity_projection = 6`: a driver returns true only when its Save/Create and `PageMetadata` round-trip the scalar as one successful logical write. Existing `ListSessionsRequest`, paging, totals, and cursor fields do not change. `GetCompatibilityInfoResponse.features` adds the stable `session_activity_inventory` identifier only when `ListSessions` can project this field from its configured store.
- **Exported Go APIs / interfaces:** Add `session.ActivityState`, `ActivityUnknown = ""`, `ActivityDraft = "draft"`, `ActivityActive = "active"`, and `session.ActivityOf([]session.Message) session.ActivityState`. Add `Activity session.ActivityState` to `port.SessionDiscoveryMeta`. `ActivityOf` is pure over a successfully decoded message slice and returns draft or active only, exactly according to `IsGenuineUserPrompt`; metadata readers normalize absent, corrupt, unavailable, unsupported, or unproved projection to unknown. `port.SessionMeta` remains source-compatible and rows available only through it normalize to unknown.
- **Tool schemas:** None — no model-facing tool input/output schema changes.
- **CLI / config:** None — no flag or configuration key changes. Feature-supporting `--resume-latest` considers active eligible main rows only; exact `--resume` remains activity-agnostic. Mecatui adds a feature-gated Drafts tab, not a CLI selector.
- **Events / persistence:** The harness derives activity with `engine/session/title.go` (`IsGenuineUserPrompt`) during every normal Create/Save. A successful Create/Save publishes its snapshot and activity projection together; an error publishes neither as the new version. JSONL metadata/catalog rows and Redis metadata-index members carry the scalar through their existing write paths; Redis retains its snapshot/index atomicity. Driver support is capability-gated: unsupported/older drivers retain opaque Save/Load behavior but yield unknown discovery activity. The canonical snapshot gains no duplicate field. Missing legacy projection is unknown; no rebuild or migration is introduced. Page order, owner filtering, total count, cursor scope, generation behavior, and bounded response behavior are unchanged.
- **Security / authority:** Inventory remains owner-authorized and content-free. Activity contains no prompt text, title source, transcript, private placement, credential, or continuation/deletion authority. List, close, failed resume, and stale observations never delete a draft; existing server-side action authorization and revalidation remain authoritative.
- **Compatibility / migration:** Additive engine and protobuf changes, with `task api:update` and an Added/minor `engine/CHANGELOG.md` entry. Old clients omit the new summary field and retain their historical inventory/latest-selection behavior. A newer mecatui against a server without `session_activity_inventory` hides Drafts, retains its historical mixed Chats view, and applies only its narrow empty-transcript latest-resume guard; it never treats an absent or unrecognized field as active. Exact-ID resume remains available in every version pairing.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — activity has one persisted-history meaning

The domain classifier uses the existing genuine-user predicate, including its exclusion of
harness-authored compaction summaries, rather than a client-local approximation. This follows
the persisted-history boundary in [ADR 0334](../adr/0334-draft-aware-session-inventory.md).

**Acceptance:**
- AC1.1: A valid empty conversation is `draft`; a valid conversation with at least one message satisfying `IsGenuineUserPrompt` is `active`, including an empty user message when that predicate classifies it as genuine.
  - verify: `TestDraftAwareSessionInventory_Scenario1_ClassifiesEmptyTextAndMultimodalHistory`
- AC1.2: A user-role synthesized compaction summary does not make an otherwise empty history active.
  - verify: `TestDraftAwareSessionInventory_Scenario1_CompactionSummaryDoesNotActivateDraft`
- AC1.3: Successful `ActivityOf` inspection returns only draft or active; unknown is reserved for unavailable or unproved discovery metadata.
  - verify: `TestDraftAwareSessionInventory_Scenario1_ValidHistoryIsNeverUnknown`

### Scenario 2 — cheap discovery carries one safe projection

Normal writes project activity without making page reads inspect whole conversations. The
existing owner-before-page, deterministic ordering, and bounded-response guarantees from
[ADR 0217](../adr/0217-session-discovery-continuation.md) remain unchanged.

**Acceptance:**
- AC2.1: Memstore, JSONL, and Redis return draft or active metadata matching the latest successfully saved conversation through their existing discovery paths; a failed Save/Create publishes neither a new snapshot nor a mismatched activity projection.
  - verify: `TestDraftAwareSessionInventory_Scenario2_StoresProjectActivityAtomically`
- AC2.2: The harness sends the same activity scalar on remote-driver Save/Create only when the driver advertises `activity_projection`; a supporting driver discovery round-trips it without decoding the opaque snapshot payload, while an unsupported/older driver yields unknown activity.
  - verify: `TestDraftAwareSessionInventory_Scenario2_DriverActivityCapabilityAndOpaqueRoundTrip`
- AC2.3: Missing, corrupt, unavailable, unsupported, or legacy metadata is unknown rather than silently draft; a later normal write may establish the projection.
  - verify: `TestDraftAwareSessionInventory_Scenario2_LegacyProjectionFailsClosed`
- AC2.4: Activity projection leaves existing owner filtering, page order, total counts, cursor scope/generation, concurrent-save best-effort behavior, and bounded Redis/JSONL discovery behavior unchanged.
  - verify: `TestInvariant_session_metadata_activity_projection_preserves_paging_contract`

### Scenario 3 — drafts leave Chats without becoming unreachable

Mecatui uses the existing complete progressively loaded inventory and locally groups received
rows. It does not claim a global tab total while pages are still loading. The feature vocabulary
follows [ADR 0248](../adr/0248-sdk-compatibility-and-error-contract.md), allowing safe mixed
client/server versions.

**Acceptance:**
- AC3.1: Feature-supporting server summaries map draft, active, and unknown activity to mecatui without conversation content; absent, malformed, and unrecognized non-empty activity strings normalize to unknown. A newer client against a server without the feature hides Drafts and retains the mixed Chats view.
  - verify: `TestDraftAwareSessionInventory_Scenario3_FeatureGatedSummaryMapping`
- AC3.2: Chats contains active and unknown main rows, Drafts contains known-draft main rows rendered `New — no messages`, and scheduled, child, other, and storage-health categories retain their current membership and actions. Progressive paging preserves rendered rows after a later-page failure, stops on cancellation, and restarts a stale generation without mixing rows.
  - verify: `TestDraftAwareSessionInventory_Scenario3_LocalDraftGroupingRenderingAndPaging`
- AC3.3: `--resume-latest` selects the newest eligible active main row, skips draft and unknown rows, rejects a selected active row whose authoritative transcript is empty or unavailable, then continues scanning older eligible active rows. It falls back to a new session only when no qualifying row exists; list/transport failures still surface.
  - verify: `TestDraftAwareSessionInventory_Scenario3_LatestResumeUsesActivityAndTranscriptGuard`
- AC3.4: Exact draft resume and explicit draft deletion remain available; listing, Close, failed resume, and stale-row observations neither delete nor mutate a draft.
  - verify: `TestDraftAwareSessionInventory_Scenario3_DraftActionsRemainExplicitAndNonDestructive`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Delay persistence until the first prompt | never under this plan | A created session remains an acknowledged durable resource. |
| Make `EndSession`/Close destructive | never under this plan | Close is local runtime teardown, not session deletion. |
| Server-side kind/activity filters, cursor filter scope, or backend secondary indexes | scale-driven inventory follow-up | Existing local grouping and progressive loading solve the current UI friction. |
| Legacy projection rebuild/migration | operational follow-up if evidence warrants it | Missing projection is safely unknown and normal writes backfill it. |
| Automated draft retention or deletion | separate retention proposal | Deletion requires its own serialization, liveness, and conditional-deletion contract. |
| Hide drafts so completely that only an exact ID can recover them | never under this plan | Drafts remain in a discoverable tab. |
| Change scheduled, delegated, or debug continuation authority | separate taxonomy work | ADR 0217 session-kind rules remain in force. |

## Definition of done

1. `task generate`, `task api:update`, `task lint`, `task test`, `task api:check`, `task docs`, and `task site:build` pass.
2. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. The owning user documentation and `docs/tui.md` explain durable drafts, exact resume, active-only latest resume, Chats/Drafts grouping, mixed-version fallback, and Delete-versus-Close semantics.
5. The implementation PR links the approved Plan / Interface PR and reports engine API, public/driver wire, store, and mixed-version conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Automated retention remains deferred: no observation is deletion authority.
- Legacy rows stay unknown until a normal write supplies the projection. They remain visible and explicitly resumable but are not selected automatically by a feature-supporting client.
- If inventory size or progressive client grouping proves inadequate, a later proposal may introduce server-side filtering with its own storage-index and cursor contract.
