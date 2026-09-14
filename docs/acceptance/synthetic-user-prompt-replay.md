# Synthetic user-prompt replay — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — this changes the exported engine event payload, durable event meaning, protobuf replay contract, and mecatui transcript projection.
**Decision record:** [ADR 0337](../adr/0337-synthetic-user-prompt-origin.md)
**Phase:** durable session replay fidelity
**Status:** landed, 2026-09-14. Implemented and verified in this Implementation candidate; authoritative when the PR merges.
**Delivery:** Split. The change crosses the engine API, event persistence, protobuf, and mecatui replay UI, so separate interface review is valuable.
**Expected tasks:** 2
**Issue:** [#1457](https://github.com/stacklok/mecatl/issues/1457)
**Plan PR:** [#1478](https://github.com/stacklok/mecatl/pull/1478)
**Approved baseline:** `95ad677fc0a7ab358ed8134e44b982f18a98e5b2`

A durable `user_prompt` event must distinguish a principal-authored prompt from a user-role
continuation authored by the harness. Mecatui replay uses that structured origin to preserve real
prompts as user bubbles while rendering harness continuations as notices, without changing the
conversation the model receives or event-sourced reconstruction.

The shared genuine-user predicate must also exclude the harness's established no-progress nudge
and background notice vocabulary so title and activity consumers do not mistake those messages
for principal instructions. This narrows [ADR 0038](../adr/0038-event-sourced-rehydration.md)'s
previous origin-opaque event decision as recorded by [ADR 0337](../adr/0337-synthetic-user-prompt-origin.md).

## Human decisions

None — issue #1457 explicitly selects an emission-time synthetic discriminator, additive protobuf projection, mecatui notice rendering, and the shared genuine-user predicate as the fix.

## Interface contract

- **gRPC / protobuf:** `UserPrompt` gains `bool synthetic = 3`. `false` means a genuine or legacy origin; `true` means the harness authored the user-role continuation. `Event.user_prompt = 17`, RPC methods, replay ordering, live-wire filtering, and the existing delivery-note exception do not change.
- **Exported Go APIs / interfaces:** `session.UserPromptPayload` gains `Synthetic bool`. `session.IsGenuineUserPrompt(session.Message) bool` retains its signature and additionally returns false for the exact established no-progress nudge texts and harness background notice form, as well as synthesized compaction summaries. No port interface changes. Mecatui's internal `client.UserPromptMsg` gains `Synthetic bool`; no exported stable client API is introduced.
- **Tool schemas:** None — no model-facing tool input, output, or availability changes.
- **CLI / config:** None — no flag, command, key, default, or precedence changes.
- **Events / persistence:** Every newly emitted `EvUserPrompt` sets origin explicitly at the single emission seam: principal prompts and committed steer input use `Synthetic:false`; `recordContinuation` paths use `Synthetic:true`, including no-progress nudges, background-pending/completion notices, and pending harness delivery continuations. Event-log JSON persists the additive field; `eventsource.Fold` continues reconstructing the same user-role `Message` sequence and does not persist origin into `Message` or snapshots. The relay continues skipping ordinary `EvUserPrompt` live and replaying it from the durable log; scheduled fire delivery keeps its existing live exception and dedicated client projection.
- **Security / authority:** Origin is server-authored at the engine emission site and grants no permission, trust, ownership, continuation, or content authority. The client does not infer synthetic origin from prompt text. Existing child-event isolation and producer-text UTF-8 repair remain unchanged.
- **Compatibility / migration:** Additive engine and protobuf changes require `task api:update`, an Added/minor `engine/CHANGELOG.md` entry, and regenerated contracts. Older clients ignore `synthetic` and retain historical rendering. New clients interpret absent/false as genuine, so legacy stored events remain readable but cannot retroactively distinguish harness continuations and may retain the old bubble rendering. No event-log rewrite or snapshot migration is introduced.

## In scope — 2 scenarios, in implementation order

### Scenario 1 — durable events carry authoritative prompt origin

The loop already separates genuine prompt ingress from its harness-owned continuation helper in
`engine/agent/loop.go` (`emitUserPrompt`, `recordContinuation`). Consistent with
[ADR 0337](../adr/0337-synthetic-user-prompt-origin.md), the event payload records that known fact
instead of asking replay clients to parse untrusted prompt content.

**Acceptance:**
- AC1.1: A newly recorded principal prompt, including multimodal input, and a committed steer emit `EvUserPrompt` with `Synthetic:false`; every harness continuation emitted through `recordContinuation` emits the same text and parts semantics with `Synthetic:true`.
  - verify: `TestSyntheticUserPromptReplay_Scenario1_EmissionOriginIsExplicit`
- AC1.2: Event-log JSON and the protobuf mapper preserve `Synthetic:true`, while an absent/false legacy value remains false; event-sourced folding reconstructs the same ordered conversation regardless of the flag.
  - verify: `TestSyntheticUserPromptReplay_Scenario1_PersistenceProtoAndFoldRoundTrip`
- AC1.3: `IsGenuineUserPrompt` rejects synthesized compaction summaries, both established no-progress nudge texts, and harness background notices, while retaining genuine ordinary, empty-text, and multimodal user messages and rejecting non-user roles.
  - verify: `TestSyntheticUserPromptReplay_Scenario1_GenuinePredicateRecognizesHarnessContinuations`
- AC1.4: Child prompts remain confined to child streams and the parent durable log gains no child-authored `EvUserPrompt` content or origin metadata.
  - verify: `TestInvariant_synthetic_user_prompt_preserves_child_event_isolation`

### Scenario 2 — mecatui replay does not invent user bubbles

Replay remains a projection of the full durable timeline described by
[the architecture guide](../architecture.md). The client carries the structured origin through
its proto-to-message mapping, and the stored-session transcript uses the existing notice style
for harness-authored continuations.

**Acceptance:**
- AC2.1: After preserving the existing delivery-note precedence, mecatui maps any remaining `user_prompt` to `client.UserPromptMsg{Text, Parts, Synthetic}` without parsing its text; false preserves the existing prompt/media projection and true carries the same content with its structured origin.
  - verify: `TestSyntheticUserPromptReplay_Scenario2_ClientMapsStructuredOrigin`
- AC2.2: Stored-session replay sends `UserPromptMsg{Synthetic:true}` to the persistent `conversation.addNotice` path and does not add a user bubble; `Synthetic:false` remains a user bubble, including its media descriptors. This is transcript content, not the transient no-progress footer status.
  - verify: `TestSyntheticUserPromptReplay_Scenario2_TranscriptRendersNoticeNotUserBubble`
- AC2.3: A scheduled fire delivery retains `DeliveryNoteMsg` and its dedicated delivery card whether `synthetic` is true or false; delivery recognition precedes generic synthetic-notice routing.
  - verify: `TestSyntheticUserPromptReplay_Scenario2_DeliveryProjectionTakesPrecedence`
- AC2.4: gRPC and HTTP replay expose the additive synthetic field while ordinary live Converse continues to suppress non-delivery `user_prompt` events and the scheduled-delivery live exception remains unchanged.
  - verify: `TestSyntheticUserPromptReplay_Scenario2_ReplayAndLiveRelayCompatibility`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Rewrite legacy event logs to infer origin | future migration only if evidence warrants it | Prompt text is untrusted and insufficient for authoritative retroactive classification. |
| Persist origin on `session.Message` or in snapshots | separate conversation-schema proposal | The bug is in durable event replay projection; model-visible history remains unchanged. |
| Change scheduled fire delivery cards | separate scheduler UX work | The existing dedicated delivery projection takes precedence and is preserved. |
| Change live chat rendering | never under this plan | The live client already owns the principal prompt and ordinary `EvUserPrompt` remains log-only. |
| Add a new event type for harness continuations | separate event-taxonomy proposal | One additive discriminator preserves ordering and event-sourced reconstruction with less compatibility cost. |

## Definition of done

1. `task lint`, `task test`, `task docs`, `task api:check`, `task generate`, and `task site:build` pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. The implementation updates the owning replay/API guidance in `user-docs/`, links this Plan / Interface PR and approved commit, and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Legacy events have no authoritative origin bit. Treating their protobuf default as genuine is the compatibility-safe choice, but an old no-progress nudge can still appear as a user bubble until new events replace that history or a separately approved migration exists.
- `IsGenuineUserPrompt` remains a predicate over `Message`, whose schema has no origin field. Its compatibility exclusion therefore recognizes the harness-owned continuation vocabulary; emission-time `Synthetic` remains the authoritative client-facing origin and must not be inferred from text.
