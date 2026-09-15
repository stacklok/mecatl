# ADR 0038 — Event-sourced SessionStore rehydration (the reference fold)

- Status: Accepted
- Date: 2026-06-19
- Scope: `engine/adapter/eventsource` (the reference fold), the `port.SessionStore`
  reconstruction contract, `engine/COMPATIBILITY.md`
- Superseded by: [ADR 0337](./0337-synthetic-user-prompt-origin.md), accepted, for Decision 3's origin-opaque `EvUserPrompt` payload and absence from public replay projection only; all other decisions remain in force
- Relates: [ADR 0027 — cloud-native](./0027-cloud-native.md) (closes the
  reconstruction half of List-2 row 11), [ADR 0036 — engine module](./0036-engine-module.md),
  [ADR 0037 — engine stability contract](./0037-engine-stability-contract.md)

## Context

mecatl persists a session as a **snapshot** (`engine/adapter/sessnap` — the JSON DTO
the in-memory and JSONL store adapters round-trip), and `port.SessionStore.Load`
deserializes it. Its own resume always reloads from that snapshot.

A host embedding the engine, however, may have an **append-only event log as its
system of record** rather than a snapshot store. the downstream consumer is the motivating consumer:
it already keeps the durable event timeline (the same `port.EventLog` stream the
gRPC/HTTP relay records — ADR 0027 Phase 3a), and would rather implement
`SessionStore.Load` by **folding** that stream back into a `*session.Session` than
maintain a parallel snapshot it does not otherwise need.

ADR 0027 shipped the durable **recording** of the event stream (List-2 row 11) and a
Phase 3b **reconstruct gate** that proves a session is forensically reconstructible
from `SessionStore.Load` + `EventLog.Read`. But it left the **reconstruction
direction** — turning a raw event stream *into* a live aggregate — as snapshot-only:
there was no documented contract for it and no reference implementation. #115 closes
that.

The shaping constraint surfaced during design: **the event stream does not carry the
assistant message's opaque replay state.** `Message.Reasoning` (the provider reasoning
replay blob), `Message.ProviderPhase` (the OpenAI Responses phase marker), and
`ToolCall.ItemID` (the provider item id) reach the conversation **only** via
`Session.RecordAssistant` in the loop — never via an emitted event. (`EvReasoningDelta`
carries a display *summary*, which the loop deliberately never stores on
`Message.Reasoning`.) So a pure event fold reconstructs the **structural** conversation
faithfully but is byte-identical-replay faithful **only** for providers that do not use
those fields (plain chat, the mock provider). For a reasoning provider the snapshot —
which carries them — is the byte-identical path, which is exactly why mecatl's own
resume uses the snapshot.

## Decision

1. **Ship a reference fold, not a SessionStore wrapper.** `engine/adapter/eventsource`
   exposes `Fold(meta SessionMeta, events iter.Seq2[session.Event, error])
   (*session.Session, error)` — a pure function from an event stream to an aggregate.
   It is an **excluded reference adapter** (no public-API stability promise, ADR 0037),
   the same posture as `sessnap`/`memstore`. A host wires its own `SessionStore.Load`
   over it; the engine ships no `EventLog`→`SessionStore` adapter.

2. **Creation metadata is an input, not an event.** The id, mode, limits, workspace,
   profile, provider/model selector, and createdAt that no event carries are supplied
   via `eventsource.SessionMeta`. We deliberately do **not** add an `EvSessionCreated`
   event to the taxonomy — the caller created the session and already holds these
   facts, so an input struct is the smaller, non-wire-widening shape. (A future
   `EvSessionCreated` is noted below as an option, not taken.)

3. **Record user-role turns durably (`EvUserPrompt`); fold reconstructs the complete
   conversation.** The loop emits a new **log-only** `EvUserPrompt` event (carrying
   `UserPromptPayload` — the user-message Text + Parts) at **every** site it records a
   user-role message: the genuine client prompt (`recordPrompt`) AND the harness-authored
   synthetic continuations (the no-progress nudge, the background-pending nudge, the
   background-completion notice), routed through one record-then-emit helper so none is
   missed. The relay appends it to the durable `port.EventLog` and **skips it on the live
   client wire** (the EvApproval / EvCompactionArchive log-only precedent — the client
   already holds its own prompt; no proto enum, no `task generate`). The fold reconstructs
   the conversation in stream order: user turns (from `EvUserPrompt`), assistant text +
   tool calls (`EvMessageDelta` / `EvToolCall`), tool results (`EvToolResult`), and the
   pre-compaction head (`EvCompactionArchive`), paired so `ValidateToolPairing` holds; plus
   cumulative `Usage` (the **sum** of every per-run `EvResult.Usage`), the lifecycle
   `State`/stop, the latest-run `Counters`, and a trailing pending ask. This **closes the
   durable-log "show what the user asked" gap** (ADR 0027 row 11). The conversation is now
   complete **except** the provider-private replay fields: the fold does **not**
   reconstruct `Message.Reasoning` / `Message.ProviderPhase` / `ToolCall.ItemID` (they are
   not event-carried), and it never places the `EvReasoningDelta` summary on
   `Message.Reasoning`. That single residual boundary is documented in
   `engine/COMPATIBILITY.md` and on `port.SessionStore` — **not** worked around by
   eventing the opaque replay blob.

4. **Reuse the state-driving logic, do not copy it.** The terminal/awaiting transition
   vocabulary lives in one place: `sessnap.RestoreState` (extracted from
   `Snapshot.Restore`) is shared by snapshot rehydration and the fold. The fold handles
   only the one case `RestoreState` cannot express generically — an **awaiting** session
   whose history legitimately ends on a dangling tool call (the loop records the
   assistant message before dispatch pauses on the ask) — by driving the trailing turn
   through the running aggregate (`BeginTurn` → `RecordAssistant` → `PauseForApproval`),
   exactly as the live loop reached that state.

5. **The contract is documented as a field-by-field table** in
   `engine/COMPATIBILITY.md` ("Session reconstruction contract"): the MUST-round-trip
   set, the run-scoped/safe-to-lose set, the creation-metadata-not-in-events fact, and
   the replay-fidelity limitation.

## Consequences

- **Event-log-SoR hosts get a first-class, documented rehydration path.** the downstream consumer can
  implement `Load` by folding its log, with a clear contract for what it must carry and
  what it may drop.
- **The reconstruction half of ADR 0027 List-2 row 11 is closed** (recording shipped in
  3a; the reference reconstruction ships here). The Phase 3b reconstruct *gate* proved
  reconstructibility; this is the reusable *implementation* of it for the event-log case.
- **The durable log now shows WHAT THE USER ASKED.** `EvUserPrompt` records every
  user-role message the loop adds, so a from-log reconstruction (and the Phase 3 gate)
  surfaces the user's prompt text — the gap ADR 0027 row 11 left open is closed. Child
  isolation holds (gauntlet #7): a child's prompt is emitted on the child run's stream
  (drained inside the delegation tool) and never reaches the parent log.
- **The contract has ONE honest, named boundary.** The conversation a fold rebuilds is
  complete except for the provider-private replay fields; byte-identical replay across
  the fold is therefore scoped to non-reasoning providers. A host wanting byte-identical
  replay for a reasoning provider must carry `Reasoning`/`ProviderPhase`/`ItemID` in its
  own richer event schema — the engine does not pretend the fold gives it for free.
- **The event taxonomy widened by one log-only event** (`EvUserPrompt`), a guarded
  `engine/session` API change (handled via the #114 workflow: `task api:update` reseeds
  `engine/api/session.txt` + a classified CHANGELOG entry). It mirrors the existing
  log-only events exactly (no proto enum, no wire surface, skipped on the client wire).
  The fold, `SessionMeta`, and `sessnap.RestoreState` remain excluded reference adapters.

### Options considered and not taken

- **An `EvAssistantMessage` event carrying the opaque replay blob.** This would let a
  pure fold be byte-identical for reasoning providers too. Rejected (the deferred
  future): it would carry the provider-private `Reasoning`/`ProviderPhase`/`ItemID` blob
  on the wire for a benefit only an event-log-SoR host that ALSO uses a reasoning provider
  needs, and such a host can carry the fields in its own schema. Recorded here as the
  natural future if that need becomes real — note this is a DIFFERENT trade-off from
  `EvUserPrompt`, which carries only the user's own input (no provider-private opaque
  state), which is why `EvUserPrompt` was taken and `EvAssistantMessage` deferred.
- **An `EvSessionCreated` event for the creation metadata.** Rejected: the caller holds
  those facts, so an input struct (`SessionMeta`) is smaller and adds no wire surface.

## See also

- [`engine/COMPATIBILITY.md`](../../engine/COMPATIBILITY.md) — the field-by-field
  reconstruction contract and the replay-fidelity limitation.
- [ADR 0027 — cloud-native](./0027-cloud-native.md) — the durable event log (Phase 3a)
  and the reconstruct gate (Phase 3b) this builds on; List-2 row 11.
- `docs/design/IMPLEMENTATION-NOTES.md` — the event-log subsystem note.
