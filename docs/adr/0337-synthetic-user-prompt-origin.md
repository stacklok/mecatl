# ADR 0337 — Classify synthetic user-prompt origin at emission

- Status: Proposed
- Date: 2026-09-14
- Scope: durable user-prompt events, replay projection, and genuine-user classification
- Supersedes: ADR 0038 Decision 3 only where `EvUserPrompt` was origin-opaque and absent from the public replay projection; its complete-conversation fold and all other decisions remain in force
- Superseded by: None

## Context

ADR 0038 made `EvUserPrompt` the durable record for every user-role message: both input from the
principal and synthetic continuations authored by the harness. That is correct for event-sourced
conversation reconstruction, but the payload does not retain origin. Live clients already own the
prompt they submitted and ordinary `EvUserPrompt` is log-only. A replay client instead receives
all of these events and currently renders each one as a user chat bubble, making no-progress
nudges and background notices appear to be text the operator typed.

Prompt text cannot safely recover origin. A principal may type the same words as a harness nudge,
and future harness messages may change. The loop already has the authoritative distinction at
emission: genuine ingress and committed steer input use the direct prompt path, while
harness-authored continuations use the shared continuation path. The event must carry that fact.

The existing `session.IsGenuineUserPrompt` predicate is also used by title and activity consumers
that inspect persisted `Message` values, which do not carry event origin. It already excludes
synthesized compaction summaries but currently accepts the established harness nudge and notice
vocabulary as genuine.

## Decision

1. Add an additive `Synthetic` boolean to `session.UserPromptPayload` and protobuf `UserPrompt`.
   Set it at the engine's single event-emission seam: principal prompts and committed steer input
   are false; messages recorded through the harness continuation helper are true. The event type,
   ordering, log append, and reconstructed conversation stay unchanged.
2. Replay clients use the structured flag, never prompt-text inference, to distinguish generic
   harness continuations from principal prompts. Mecatui carries it on its internal
   `client.UserPromptMsg` and routes a synthetic message through the stored transcript's persistent
   `conversation.addNotice` presentation rather than a user bubble; it does not reuse the transient
   no-progress footer status. The scheduled fire delivery discriminator retains precedence and its
   dedicated card on both replay and its narrow live relay exception.
3. Keep protobuf zero-value compatibility. Absent or false means genuine or legacy-unknown; true
   is authoritative synthetic origin. Do not rewrite old event logs or guess their origin.
4. Widen `session.IsGenuineUserPrompt` to reject the loop's established no-progress nudge texts and
   harness background notice form in addition to synthesized compaction summaries. This is a
   compatibility classifier for persisted `Message` values, not the replay origin authority.
   Ordinary, empty-text, and multimodal user messages retain the existing genuine classification.
5. Do not add origin to `session.Message`, snapshots, tool schemas, configuration, or permission
   policy. Synthetic user-role messages remain part of model-visible history and continue to fold
   identically; this decision changes only durable origin metadata and human replay projection.

## Consequences

New durable logs distinguish operator-authored prompts from harness-authored continuations without
parsing producer-controlled text. Mecatui replay no longer invents a user bubble for a known
synthetic message, while scheduled delivery cards and genuine multimodal prompts retain their
existing presentation.

The exported engine event payload and protobuf surface grow additively, requiring the engine API
compatibility workflow and contract regeneration. Old clients ignore the new field. New clients
read old events as false because protobuf and JSON zero values cannot distinguish legacy unknown
from genuine; therefore legacy logs remain readable but keep the historical rendering ambiguity.

The shared genuine-user predicate necessarily uses stable harness vocabulary because `Message`
does not persist origin. That can conservatively reject a principal message exactly matching a
harness nudge in title/activity fallback code. The authoritative replay decision does not share
that ambiguity because it uses the emission-time flag.

## Rejected alternatives

- **Infer origin in mecatui from prompt text.** Prompt text is untrusted, creates false positives,
  and duplicates engine vocabulary in a client.
- **Drop synthetic prompts from the event log or fold.** They are model-visible conversation turns;
  removing them makes event-sourced reconstruction diverge from snapshots.
- **Introduce a second event type.** An additive discriminator preserves the existing ordered fold
  and compatibility surface with less taxonomy churn.
- **Add origin to every `session.Message`.** That widens snapshots and conversation APIs beyond the
  replay bug and is unnecessary for model behavior.
- **Migrate old logs by matching known strings.** The result would claim authority the historical
  data does not contain.

## See also

- [Synthetic user-prompt replay acceptance plan](../acceptance/synthetic-user-prompt-replay.md)
- [ADR 0038 — Event-sourced SessionStore rehydration](0038-event-sourced-rehydration.md)
- [ADR 0075 — Fire-result delivery](0075-fire-result-delivery.md)
- [Architecture guide](../architecture.md)
