# Session continuity UX — acceptance plan

**Phase:** durable session discovery, inspection, continuation, and CLI handoff  
**Status:** landed, 2026-08-14. Aggregate implementation plus one panel repair wave complete.  
**Issues:** [stacklok/mecatl#471](https://github.com/stacklok/mecatl/issues/471), [stacklok/mecatl#473](https://github.com/stacklok/mecatl/issues/473), [stacklok/mecatl#525](https://github.com/stacklok/mecatl/issues/525).  
**ADR:** [ADR-0217](../adr/0217-session-discovery-continuation.md) — durable session taxonomy, authoritative transcript, bounded inventory, and continuation safety.<br>
**Accumulator branch:** `acc/session-continuity-ux` (off latest `origin/main`).

The smallest set of work that lets an operator identify the active session, discover and
inspect every stored session family honestly, continue a prior main chat without hidden
context, and resume it directly from the command line. Delegated children remain inside
their parent-owned lifecycle; this plan does not invent whole-team or Parallel resume.

## Why these scope cuts

- [ADR-0217](../adr/0217-session-discovery-continuation.md) keeps IDs opaque, makes kind/capabilities server-authored, and uses a snapshot-derived transcript for continuation.
- [ADR-0038](../adr/0038-event-sourced-rehydration.md) separates authoritative snapshot state from optional EventLog activity replay.
- [ADR-0065](../adr/0065-conversation-fork.md) keeps peer forking distinct from continuing the same chat and deliberately omits fork lineage.
- [ADR-0015](../adr/0015-background-subagents.md) keeps Subagent resume parent/model-owned and Parallel/team children inspect-only from mecatui.
- [`AGENTS.md` — run-entry recovery and child-session guards](../../AGENTS.md) remains authoritative: top-level prompts recover via `loadAndReopen`; delegated children cannot be driven directly.

## In scope — 7 scenarios, in implementation order

### Scenario 1 — Trusted producers stamp a durable session taxonomy

A test host creates every built-in session family and round-trips it through every in-tree
store/source path. The taxonomy is inert metadata, but the server uses it in addition to the
legacy prefix guard when deciding whether a public chat prompt may drive the aggregate.
This follows [ADR-0217](../adr/0217-session-discovery-continuation.md), [the domain model](../architecture/domain-model.md), and [the ports chapter](../architecture/ports.md).

**Acceptance:**
- AC1.1: Main, scheduled, Subagent, Parallel-branch, and team-member sessions round-trip the exact kind and relationship fields required by ADR-0217 through snapshot, metadata-list, event-source creation metadata, and every in-tree store/driver adapter.
  - verify: `TestSessionContinuityUX_Scenario1_KindRelationshipRoundTrip`
- AC1.2: Public create/fork/carryover requests can create only `main`; callers cannot stamp or override scheduled/child relationships.
  - verify: `TestADR_0108_PublicCreateCannotForgeKind`
- AC1.3: Invalid kind/relationship combinations fail closed at construction or restore rather than granting replay/continuation capabilities.
  - verify: `TestADR_0108_InvalidRelationshipsFailClosed`
- AC1.4: Peer forks, effort forks, and model carryovers remain `main` and do not acquire lineage fields, preserving ADR-0065.
  - verify: `TestSessionContinuityUX_Scenario1_PeerRebindsRemainMain`
- AC1.5: The work retains the existing `session.New` signature and required `SessionStore` interface; every intentional engine API change is additive and classified `Added`, with no `Changed` or `Removed` baseline entry.
  - verify: `TestInvariant_session_continuity_api_is_additive`

---

### Scenario 2 — Direct-drive safety is kind-aware and purpose-separated

Public chat prompts and trusted scheduler fires converge on the existing run-entry machinery
after different kind gates. Delegated and scheduled sessions cannot be driven through the
public chat surface, including custom-ID children, while the scheduler remains able to drive
its own fire. Legacy prefixes remain a defense-in-depth compatibility gate under
[ADR-0217](../adr/0217-session-discovery-continuation.md) and [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC2.1: Public `StartRunContent` rejects every explicitly-stamped Subagent, Parallel-branch, team-member, and scheduled session regardless of ID spelling.
  - verify: `TestInvariant_non_main_sessions_cannot_start_as_chat`
- AC2.2: The trusted scheduler-purpose entry drives an explicitly-stamped scheduled session through the same downstream run loop, while a public caller cannot claim that purpose.
  - verify: `TestADR_0108_SchedulerPurposeOnlyDrivesScheduled`
- AC2.3: For legacy snapshots, any historical child or scheduled prefix denies public chat continuation even when kind is missing or conflicting; missing/invalid/conflicting metadata never grants a capability.
  - verify: `TestADR_0108_LegacySafetyGate`
- AC2.4: A normal explicitly-stamped main session with an ordinary opaque ID remains continuable; no client-side prefix parser participates in the decision.
  - verify: `TestInvariant_session_ids_are_not_client_classifiers`
- AC2.5: Unknown/pruned, ownerless-under-enforcement, and foreign-owned exact IDs remain one indistinguishable NotFound-class result across get, list, transcript, and startup-resume surfaces.
  - verify: `TestSessionContinuityUX_Scenario2_OwnershipOracleClosed`

---

### Scenario 3 — The server exposes an authoritative transcript and bounded inventory

A client obtains one coherent snapshot-derived transcript matching the conversation the next
model request will use. EventLog remains an independently-labelled optional activity replay;
EOF is never treated as transcript completeness. Inventory is cursor-bounded and stable.
This implements [ADR-0217](../adr/0217-session-discovery-continuation.md) without weakening
[ADR-0038](../adr/0038-event-sourced-rehydration.md).

**Acceptance:**
- AC3.1: The public transcript surface performs one ownership-checked SessionStore load and returns the exact human-displayable `Conversation.Messages` from that coherent aggregate, including a genuinely-empty idle session as complete with zero messages; it performs no environment resolution, engine rebuild, lease, or persistence.
  - verify: `TestSessionContinuityUX_Scenario3_AuthoritativeTranscript`
- AC3.2: Unknown, corrupt, and failed snapshot loads return typed errors; they never appear as a successful empty transcript.
  - verify: `TestADR_0108_TranscriptAbsenceIsNotEmptySuccess`
- AC3.3: Compacted sessions expose the current summary/tail the model will use, and provider-private reasoning replay blobs are neither displayed as human text nor required for transcript completeness.
  - verify: `TestSessionContinuityUX_Scenario3_CompactedAndReasoningTranscript`
- AC3.4: EventLog activity availability/completeness is reported separately; missing terminal events, append gaps, read failures, and EOF never upgrade an activity stream to an authoritative transcript.
  - verify: `TestInvariant_event_replay_never_attests_model_context`
- AC3.5: Public inventory responses use an opaque keyset cursor, enforce a maximum page size, order ties by `(modified_at DESC, session_id ASC)` after ownership filtering, and remain safe/bounded under concurrent saves without claiming snapshot-stable pages.
  - verify: `TestSessionContinuityUX_Scenario3_PaginationContract`
- AC3.6: Every in-tree durable store and remote-driver adapter passes shared optional metadata-pager conformance; a store without pager support reports listing unavailable instead of returning an unbounded response.
  - verify: `TestSessionContinuityUX_Scenario3_PagerConformance`
- AC3.7: gRPC and HTTP expose equivalent kind, relationship, capability, reason-code, pagination, and transcript behavior.
  - verify: `TestSessionContinuityUX_Scenario3_TransportParity`

---

### Scenario 4 — `/sessions` is searchable, honest, and non-destructive

An operator sees Chats, Scheduled runs, and Child runs. Inspecting a non-chat opens a
read-only overlay and closing it restores the exact active chat. Main continuation uses the
authoritative snapshot transcript, not EventLog. The TUI remains a proto-free client under
[`architecture.md`](../architecture.md).

**Acceptance:**
- AC4.1: `/sessions` groups rows by server-authored kind into Chats, Scheduled runs, and Child runs; team-member rows are not presented as resumable teams.
  - verify: `TestSessionContinuityUX_Scenario4_FamilyTabs`
- AC4.2: Each titled row shows ADR-0285's fixed ordinary session handle; the current chat stays visible with a `current` marker and cannot be redundantly opened.
  - verify: `TestPredictableSessionHandles_Scenario1_SharedNormalHandle`
- AC4.3: Ordinary handles are terminal-safe fixed projections and are never sent to server APIs as session IDs; debug uses one TARGET grammar where exact full-ID equality wins and ambiguous projections require the copied full ID.
  - verify: `TestPredictableSessionHandles_Scenario3_PresentationParitySafetyAndLayering`
- AC4.4: Filtering matches title, full ID, visible handle, model, workspace, and child relationship names case-insensitively across fetched pages.
  - verify: `TestSessionContinuityUX_Scenario4_SearchFields`
- AC4.5: Inspecting a scheduled or child run leaves the active prompt target, live subscription, capabilities, title, model, and conversation unchanged; Escape restores the prior view.
  - verify: `TestSessionContinuityUX_Scenario4_InspectionPreservesActiveChat`
- AC4.6: Snapshot transcript load failure blocks continuation and offers Retry and Back; no default path enables input with hidden context.
  - verify: `TestInvariant_transcript_failure_never_enables_hidden_context`
- AC4.7: Closed capability reason codes, not parsed prose, determine enabled actions; caller-visible text reveals no owner/lease identity or backend path.
  - verify: `TestSessionContinuityUX_Scenario4_ReasonCodes`
- AC4.8: Palette text, `?` help, overlay hints, `docs/tui.md`, and relevant `user-docs/` pages consistently say Continue for chats and Inspect for scheduled/child runs.
  - verify: inspection — `task docs` and `task site:build` pass with the reviewed wording

---

### Scenario 5 — The active session identity is visible and copyable

While connected, `/session` shows the current chat's exact metadata. Clipboard copy returns
the opaque ID byte-for-byte; terminal rendering uses a safe reversible quoted form under
[ADR-0217](../adr/0217-session-discovery-continuation.md). The header stays compact and the
affordance is discoverable under existing TUI help conventions.

**Acceptance:**
- AC5.1: `/session` displays a reversible safe representation of the full ID plus title, state, workspace, created/modified timestamps when known, and provider/model for the active chat.
  - verify: `TestSessionContinuityUX_Scenario5_DetailsSurface`
- AC5.2: One explicit action copies the byte-exact opaque ID through the clipboard abstraction and reports success/failure without claiming an empty or stale copy.
  - verify: `TestSessionContinuityUX_Scenario5_CopyExactID`
- AC5.3: Stored-session continuation, model carryover, effort fork, and worktree switch each update the details/copy target to the final adopted ID.
  - verify: `TestSessionContinuityUX_Scenario5_RebindMatrix`
- AC5.4: The compact header uses ADR-0285's fixed ordinary handle, remains width-safe, and `/session` is discoverable from slash completion and `?` help.
  - verify: `TestSessionContinuityUX_Scenario5_HeaderAndHelp`
- AC5.5: Newline/control-bearing, empty, and very long valid-UTF-8 IDs render safely while clipboard copy remains exact; a persisted invalid-UTF-8 ID is rejected as corrupt before protobuf mapping rather than repaired into a different handle.
  - verify: `TestInvariant_session_details_render_safe_copy_exact`

---

### Scenario 6 — Mecatui starts directly in a selected prior chat

The operator launches embedded or connect mode with an exact ID or asks for the latest
eligible chat. Static adoption completes before the TUI accepts input and never creates a
disposable startup session. The first prompt still performs atomic state/lease/environment
validation through the ordinary run-entry funnel in [`IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md)
and [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC6.1: `--resume <id>` and `--resume-latest` are mutually exclusive, accepted in embedded and connect modes, and accurately described in CLI help.
  - verify: `TestSessionContinuityUX_Scenario6_FlagGrammar`
- AC6.2: Successful startup resume performs no `CreateSession`, loads the authoritative transcript, adopts metadata, then enables follow-up input.
  - verify: `TestSessionContinuityUX_Scenario6_NoThrowawaySession`
- AC6.3: `--resume-latest` deterministically selects the newest owned `main` row whose authoritative transcript is available; it excludes scheduled, child, unknown, awaiting, and currently-reported-live rows without resolving an Environment or rebuilding an engine during selection.
  - verify: `TestSessionContinuityUX_Scenario6_LatestSelection`
- AC6.4: Exact resume of a crash-orphaned `running` main may display its transcript, but the client never guesses staleness; the first prompt alone may repair it after the real run-entry lock/lease proves exclusivity.
  - verify: `TestSessionContinuityUX_Scenario6_StaleRunningDefersToRunEntry`
- AC6.5: Foreign/missing/pruned targets use generic NotFound guidance; inspect-only kind and unavailable transcript fail before interactive input with closed reason codes, while environment/profile/model attachment is deferred to first prompt.
  - verify: `TestADR_0108_StartupStaticValidation`
- AC6.6: The first prompt performs environment reattachment, engine rehydration, liveness, and lease checks through the ordinary run-entry funnel; any failure leaves the adopted transcript visible/read-only and offers retry/back without creating another session.
  - verify: `TestADR_0108_FirstPromptRevalidatesAtomically`
- AC6.7: A seed prompt is submitted exactly once only after successful transcript adoption; any adoption failure submits nothing.
  - verify: `TestSessionContinuityUX_Scenario6_SeedAfterAdoption`
- AC6.8: Bare `mecatui` remains new-session-by-default.
  - verify: `TestSessionContinuityUX_Scenario6_DefaultRemainsNew`

---

### Scenario 7 — Exit leaves a safe continuation handoff

After a clean exit, the alternate screen is restored and stderr contains the final active
session handle in one documented reversible record, including in-TUI rebinding. This is a
client/composition concern under [`architecture.md`](../architecture.md), not a new engine
run API.

**Acceptance:**
- AC7.1: Clean exit emits one documented quoted/JSON-safe stderr line from which the byte-exact final active session ID can be decoded after alternate-screen teardown.
  - verify: `TestSessionContinuityUX_Scenario7_FinalIDAfterTeardown`
- AC7.2: Stored-session continuation, model carryover, effort fork, and worktree switch each cause exit to emit the final adopted ID rather than the startup ID.
  - verify: `TestSessionContinuityUX_Scenario7_FinalRebindMatrix`
- AC7.3: No-session, startup-failure, and forced-exit paths do not print a misleading successful handoff; stdout remains available for existing output and stderr grammar is stable in embedded/connect modes.
  - verify: `TestSessionContinuityUX_Scenario7_OutputContract`
- AC7.4: Exit output and startup flags are documented together as the normal “copy now or resume later” workflow.
  - verify: inspection — `task docs` and `task site:build` pass with the reviewed workflow

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Resume an entire Team or a Parallel branch | future orchestration durability work | [ADR-0014](../adr/0014-agent-teams.md), [ADR-0217](../adr/0217-session-discovery-continuation.md) |
| Prompt a child directly as a top-level chat | never under this plan | [`AGENTS.md`](../../AGENTS.md) child-session run-entry invariant |
| Treat EventLog EOF as authoritative transcript completeness | never under this plan | [ADR-0038](../adr/0038-event-sourced-rehydration.md) |
| Make bare `mecatui` resume latest by default | possible later opt-in | [ADR-0217](../adr/0217-session-discovery-continuation.md) |
| Track peer-fork/carryover lineage or add title renaming | separate follow-up | [ADR-0065](../adr/0065-conversation-fork.md) |
| Generic gRPC cross-process reattachment to awaiting approval | separate protocol feature | [`AGENTS.md`](../../AGENTS.md) awaiting-only seam |
| End/Detach remote runtime resources on clean mecatui exit | separate lifecycle issue | [ADR-0027](../adr/0027-cloud-native.md) resource inventory discipline |

## Cross-cutting deliverables

- Promote ADR-0217 to Accepted with Scenario 1; after acceptance it is frozen.
- Regenerate protobufs/contracts and the engine API baseline/CHANGELOG when applicable.
- Extend store/source/driver conformance for taxonomy and paging.
- Update `docs/architecture.md`, `docs/design/IMPLEMENTATION-NOTES.md`, `docs/tui.md`, CLI help, and relevant `user-docs/` pages in the same PR.
- Keep session retention, ownership, lease, environment reattachment, UTF-8, and child-isolation tests green.

## Sequencing recommendation

Scenarios 1–3 are the contract and safety foundation; no TUI continuation task starts before
they land. Scenarios 4 and 5 can then proceed in parallel. Scenario 6 consumes the same
server capabilities/transcript and must not add flag-specific classifiers. Scenario 7 lands
last so its handoff observes every rebind path.

Issue #473 already has an older, uncommitted worktree in this checkout. Do not edit or merge
it in place. Reuse its tests/ideas only by a deliberate transplant onto the accumulator after
Scenarios 1–4, resolving overlap against ADR-0217 rather than keeping its older classifier.

## Named tests landing in this plan

- `TestInvariant_non_main_sessions_cannot_start_as_chat`
- `TestInvariant_session_ids_are_not_client_classifiers`
- `TestInvariant_event_replay_never_attests_model_context`
- `TestInvariant_transcript_failure_never_enables_hidden_context`
- `TestInvariant_session_details_render_safe_copy_exact`
- `TestADR_0108_*`
- `TestSessionContinuityUX_Scenario1_*` through `TestSessionContinuityUX_Scenario7_*`

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` regenerates the configuration reference and the matlatl strict link gate is green.
3. `task api:check` passes, or `task api:update` and a classified `engine/CHANGELOG.md` note land.
4. `task ac-trace-strict` resolves every proof after this plan is `landed`.
5. Every named test above is grep-locatable and green.
6. `task generate` leaves contracts and generated docs clean.
7. `task site:build` passes.
8. `go run ./cmd/mecademo` still prints a full offline session.
9. Offline embedded/connect e2e coverage proves create-new, resume-exact/latest, inspect-return, copy-ID, and exit handoff.
10. `/panel-review` reports zero ship-blockers, including UX and test adequacy.

## Deferred decisions and known risks

- **Transcript projection richness.** Snapshot transcript intentionally omits transient activity and pre-compaction messages; optional EventLog activity replay may be offered separately.
- **Legacy compatibility.** The server helper is migration behavior; newly-written snapshots always carry explicit metadata and clients never learn prefixes.
- **Existing issue #473 worktree.** It is 65 commits behind the fetched base with uncommitted changes; preserve it for comparison only until coordinated.
- **External store listing.** Exact-ID operations remain available when a custom store lacks the optional pager, but inventory honestly reports unavailable.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is satisfied.
