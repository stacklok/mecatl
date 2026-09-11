# Steer-while-running — acceptance plan

**Phase:** capability — mid-run user input (steer)
**Status:** landed (reworked through review rounds, 2026-08-18). Settled in the #512 design discussion; rounds 2–3 rework landed on the same branch (Ozz review, then ad-hoc live-test findings).
**Issue:** [stacklok/mecatl#512](https://github.com/stacklok/mecatl/issues/512).
**ADR:** ADR-0232 defines the steer timing, inbox, correlation, promotion, and runtime gate; ADR-0251 defines its multimodal payload and attachment lifecycle.
**Accumulator branch:** `feat/steer-while-running` (PR #570).

The smallest set of work that lets a user inject a message into an **in-flight** run — Claude Code's "steer while running" — instead of waiting for the run to end and submitting a fresh prompt. Today mecatl's queue is purely client-side and turn-terminal ([`cmd/mecatui/ui/update.go`](../../cmd/mecatui/ui/update.go) `m.queued` / `drainQueue`): a typed line is staged and submitted as a brand-new follow-up run only when the current run ends. This plan adds an engine-side steer path so a long multi-tool run can be nudged mid-flight.

The doc is organized scenario-first because acceptance is about what the running harness can demonstrate, not which packages exist on disk.

The load-bearing enabler: mecatl's LLM adapters are **stateless** (`store:false`, full replay each turn — [`AGENTS.md`](../../AGENTS.md)), so injecting a steer is just appending a `Message{Role: user}` to the history before the next replay. **No provider API support is required** and the feature is portable across Anthropic Messages / OpenAI Responses / Chat Completions. The whole feature is a harness/loop concern: a run-scoped inbox, a turn-boundary injection seam, a wire frame, and an honest client echo.

## Why these scope cuts

- **Steer-only, never interrupt.** The steer takes effect at a turn boundary *after* the current streamed response and its tool batch settle — it never preempts or aborts an in-flight model call. Abort-and-replay would break no-replay-after-first-chunk and force retraction of already-emitted events. This matches Claude Code's after-settle behaviour. (Settled in #512.)
- **Append-after-settle, never insert-mid-history.** The steer lands *after* the just-finished tool results as a trailing user message, so the byte-stable prompt prefix ([`engine/agent/loop.go`](../../engine/agent/loop.go) `buildRequest`, assembled once per run) stays a valid cache prefix and the steer pays no prompt-cache penalty beyond normal history growth.
- **Engine inbox = a single append-default slot, not a list.** At most one pending steer *bundle* per run; a second `steer` while one is pending **appends** (`pending += "\n\n" + text`, outcome `SteerAppended`) — supersede and slot_full were dropped in the round-3 rework (Ozz finding #1 + the live-tested stuck-queue). Replacing a pending bundle is the explicit **cancel-then-resend** (`steer_cancel` on `↑` edit-back), with the client re-minting a fresh `message_id`. The TUI's ordered queue holds per-fragment ids; the drain echo's *watermark* id (the tail of the bundle's ordered id-list) splits it — the prefix drained, the suffix pending.
- **Engine is authoritative; the client renders the echoed truth.** The client never assumes what happened to its send — it renders what the engine reports drained (the `EvSteer` echo), and every frame/ack/echo carries a client-minted **`message_id`** the server echoes verbatim, so the client correlates by id (never by text) and ignores a stale ack (one whose id no longer sits in the queue). The ack reports `accepted` / `appended` / `too_late`.
- **gRPC-only v1.** The steer rides a new `Converse` oneof arm on the existing bidi stream ([`harness.proto`](../../contracts/proto/mecatl/v1/harness.proto) `Converse` is already `stream ConverseRequest → stream ConverseResponse`). The HTTP/SSE surface has no client→server mid-run channel; adding it is deferred (see *Out of scope*).
- **Main-run only.** Steer-to-child (subagent / team member / parallel branch) requires a richer parent→child communication channel than exists today and is deferred.

## In scope — 6 scenarios, in implementation order

Scenarios are listed in implementation order. Each is independently demoable; later scenarios assume earlier ones but don't change their acceptance criteria. Within each scenario, ACs progress trivial happy path → richer happy path → edges → cross-cutting.

---

### Scenario 1 — Engine steer inbox + turn-boundary injection

The core: a run-scoped, single-slot mutex inbox on `Run`, drained at the existing **Step 2a** turn-boundary seam ([`engine/agent/loop.go`](../../engine/agent/loop.go) `runLoop`), the SAME provider-legal point `injectBackgroundNotice` and `drainPendingDelivery` already use — history there always ends on a user prompt / tool result / nudge, never inside a `tool_use` pair, so the injected steer replays cleanly and survives `session.ValidateToolPairing` and compaction. The steer is recorded via `recordContinuation` ([`engine/agent/loop.go`](../../engine/agent/loop.go) — `RecordUserPrompt` + the log-only `EvUserPrompt`), exactly like the existing harness-framed continuations. See [`AGENTS.md` — the run-entry / event-log invariants](../../AGENTS.md) and [ADR-0038](../adr/0038-event-sourced-rehydration.md) (event-sourced rehydration must reconstruct the steer like any user turn).

**Work:**
- engine domain (`engine/session`): no change — the steer is ordinary `RecordUserPrompt` history.
- engine app (`engine/agent`): a `Run`-scoped steer inbox (single pending bundle, **append-default** after round 3 + cancel, mutex-guarded); a Step 2a drain (`drainPendingSteer`) ordered with the existing notice/delivery seams; the drain echo emit.
- ports (`engine/port`): no widening — the inbox is loop-internal, fed by the wire layer below.

**Acceptance:**
- AC1.1: A steer enqueued while a run is mid-flight is recorded as an ordinary user message at the next turn boundary, appears in `Conversation.Messages`, and is replayed to the model on the following turn.
  - verify: `TestSteer_InjectedAtTurnBoundary`
- AC1.2: The steer is recorded via `recordContinuation`, so the durable log captures it (log-only `EvUserPrompt`) and `eventsource.Fold` reconstructs the user turn across rehydration.
  - verify: `TestSteer_RecordedAndRehydrated`
- AC1.3: The steer is appended *after* the settled tool results, never inside a `tool_use` pair — `session.ValidateToolPairing` holds on the post-injection history.
  - verify: `TestSteer_PreservesToolPairing`
- AC1.4: The injected conversation keeps the message prefix byte-stable within the run (fragments assembled once, conversation appended), so the steer costs no prompt-cache rebuild beyond normal history growth.
  - verify: `TestSteer_KeepsPromptPrefixByteStable`
- AC1.5: A run with an empty inbox drives exactly as before (the drain is a no-op) — no behavioural change when steer is unused.
  - verify: `TestSteer_EmptyInboxNoOp`
- AC1.6: A steer drained at a boundary where compaction also fires is still replayed **verbatim** to the model on the following turn — the compaction recent-user back-snap (`snapCutToRecentUserTurn`, the "recent USER instruction must survive verbatim" invariant) keeps the just-drained user message out of the summarized head ([`AGENTS.md` — compaction must never emit unpaired history](../../AGENTS.md)).
  - verify: `TestSteer_SurvivesCompactionBoundary`
- AC1.7: A pending steer that trips the turn-limit / token-budget brake at the next boundary is recorded (durable history) and the run terminates `StopMaxTurns`/`StopBudget` normally; the recorded steer is addressed by the *next* run — it is never silently dropped and never bypasses `BeginTurn`'s bound ([`AGENTS.md` — the token budget is a turn-boundary clean terminal](../../AGENTS.md)).
  - verify: `TestSteer_BrakeTerminalKeepsRecorded`
- AC1.8: A pending (un-drained) steer is **best-effort, in-memory, and lost with the run** on a process crash/session `Abandon` — this is the explicit honest contract, not an emergent property; it is NOT persisted across restart ([`AGENTS.md` — the run-entry `Abandon` seam](../../AGENTS.md)).
  - verify: `TestSteer_PendingSteerLostOnRestart`

---

### Scenario 2 — Append-default / cancel contract (the race made observable)

At most one pending steer *bundle* per run. A second `steer` while one is pending **appends** (`pending += "\n\n"+text`, outcome `SteerAppended`) — supersede/slot_full were dropped in round 3; replacing a pending bundle is explicit **cancel-then-resend** (`steer_cancel` on `↑` edit-back). The client cannot observe the exact drain moment (stream latency), so the engine reports the outcome authoritatively rather than letting the client guess — the drain echo doubles as the recorded-history invariant that [`AGENTS.md` — the recorded == streamed == model-view rule](../../AGENTS.md) pins, and the outcome is an enum, not stacked booleans. Every frame carries a client-minted `message_id` the server echoes verbatim on the ack and the drain echo, so the client correlates by id and ignores a stale ack (id no longer in queue). See the race analysis in #512 + [ADR-0038](../adr/0038-event-sourced-rehydration.md).

**Work:**
- engine app (`engine/agent`): the inbox's `EnqueueSteerWithMessageID(text, parts, messageID) → {accepted | appended | too_late}` / `CancelSteer() → {retracted | none_pending}` / drain transitions; an enum-typed outcome. `EnqueueSteer(text, parts)` remains the id-less compatibility entry point.
- The drain emits `EvSteer` carrying the committed merged text, ordered media parts, and watermark `message_id`. The engine stores the latest contributing id in the same mutex-guarded bundle as the content.

**Acceptance:**
- AC2.1: At most one bundle is pending per run; a second `steer` while one is pending **appends** (`SteerAppended`), the pending bundle grows by append, and only the pending merged text ever drains as ONE bundle. Replacing a pending bundle is explicit: `steer_cancel` then resend — the cancel-then-recompose path.
  - verify: `TestSteer_AppendedMerges`; `TestSteer_NoSupersedePath`; `TestSteer_AppendLinearizable`
- AC2.2: `steer_cancel` on a pending bundle retracts it; the run drains nothing and the next turn sees no injected message.
  - verify: `TestSteer_CancelRetractsPending`
- AC2.3: A `steer` that arrives *after* the boundary drained (or after the run went terminal) reports `too_late` — never silently drained into a finished turn.
  - verify: `TestSteer_TooLateNotDrained`
- AC2.4: The drain emits `EvSteer` carrying the committed merged text and the watermark `message_id` (the latest id stored in that engine bundle) — the client splits its ordered queue on it. A new enqueue after the drain cannot change the prior bundle's watermark while its event awaits projection.
  - verify: `TestSteer_DrainEmitsCommittedEcho`; `TestSteer_MessageIdRoundTrip`; `TestSteer_WatermarkEchoLatestId`; `TestSteerMessageIDIsAtomicWithDrainedBundle`
- AC2.5: Enqueue/cancel/drain are safe under concurrent access — no data race, no double-drain.
  - verify: `TestSteer_InboxConcurrentSafe` (run under `-race`); `TestSteer_AppendLinearizable`
- AC2.6: The recorded steer, the streamed `EvSteer` echo, and the message the model sees have the same text and ordered parts — the recorded == streamed == model-view invariant holds (steer text is UTF-8-repaired at ingress).
  - verify: `TestInvariant_recorded_streamed_model_view`; `TestSteer_IngressUTF8Repaired`

---

### Scenario 3 — Lost terminal race → auto-promote to a fresh follow-up run

When a steer arrives for a session whose run is already terminal (the user's "still running" belief lagged the real state), the engine does **not** drop the text. It auto-promotes the steer into a fresh follow-up run through the existing hardened run-entry funnel — `loadAndReopen` + the lease + recover-if-terminal ([`internal/adapter/server/service.go`](../../internal/adapter/server/service.go) `StartRunContent`), so promotion reuses the reopen/recover discipline rather than bypassing it ([`AGENTS.md` — every run-entry path must reopen/recover/abandon](../../AGENTS.md)). Transparent to the user; an explicit contract to the engine.

**Work:**
- composition (`internal/adapter/server`): the wire steer handler routes on `IsLive` / session state — live run → enqueue to inbox; terminal → promote via the prompt funnel.
- engine app: the `too_late`/not-live outcome is the signal the service promotes on.

**Acceptance:**
- AC3.1: A steer arriving on a live run is enqueued to that run's inbox (not promoted).
  - verify: `TestSteer_LiveRunEnqueues`
- AC3.2: A steer arriving after the run went terminal is promoted to a fresh follow-up run that drives through `loadAndReopen` (recovering the terminal state) and is *not* silently dropped.
  - verify: `TestSteer_TerminalRacePromotes`
- AC3.3: Promotion reuses the run-entry funnel (recover-if-completed / interrupt-if-cancelled / recover-if-failed) — it never starts a run on an unrepaired terminal session.
  - verify: `TestSteer_PromotionUsesRunEntryFunnel`

---

### Scenario 4 — Steer while awaiting an ask (queue-only)

While a run is parked `awaiting` on a permission or plan ask, the loop is suspended in `PauseForApproval` — it is not iterating `runLoop`, so a steer cannot be drained until the ask is resolved. The steer is held and delivered *after* the verdict resumes the loop, at the resumed run's first turn boundary. The ask still requires an explicit verdict; the steer is purely additive input (the "yes-and" / "no-and" case from #512). See [`AGENTS.md` — the awaiting run-entry seam](../../AGENTS.md) and [ADR-0069](../adr/0069-plan-approval-gate.md).

**Work:**
- engine app: the inbox holds the pending steer across the parked state; `driveFromAwaiting`/`resumeFromAwaiting` re-enters `runLoop`, whose Step 2a drain picks it up at the first post-resume boundary.

**Acceptance:**
- AC4.1: A steer submitted while a run is parked `awaiting` is held, not rejected and not delivered early.
  - verify: `TestSteer_AwaitingAskIsHeld`
- AC4.2: On verdict-driven resume, the held steer is drained at the resumed run's first turn boundary and replayed to the model.
  - verify: `TestSteer_AwaitingResumeDrains`
- AC4.3: The held steer does not resolve, modify, or bypass the pending ask — the ask still requires an explicit verdict.
  - verify: `TestSteer_AskStillRequiresVerdict`

---

### Scenario 5 — Wire: gRPC `Converse` frame + capability advertisement

The steer rides the existing bidi `Converse` stream as a `ConverseRequest` oneof arm alongside `prompt` / `resume_approval` / `cancel` / `cancel_child` ([`harness.proto`](../../contracts/proto/mecatl/v1/harness.proto)). The server advertises runtime enablement through the single `ServerCapabilities.steer` bit ([`internal/adapter/server.Service.capabilities()`](../../internal/adapter/server/service.go)).

**Work:**
- contracts (`contracts/proto`): `Steer` + `SteerCancel` messages, a `steer` / `steer_cancel` oneof arm on `ConverseRequest`, the `ServerCapabilities.steer` runtime bit, and multimodal `Steer` / `EvSteer` payloads; `task generate` regenerates `contracts/gen`.
- engine app (`engine/agent`): the canonical `Run.EnqueueSteerWithMessageID(text, parts, messageID)` entry point the Service drives, the id-less `Run.EnqueueSteer(text, parts)` compatibility entry point, and the `EvSteer` event projection.
- composition (`internal/adapter/server`): the `Converse` handler routes steer frames to the live run's inbox and relays the outcome back to the client.

**Acceptance:**
- AC5.1: A client can send a `steer` frame mid-run on the `Converse` stream and observe the injected message + the `EvSteer` echo on the same stream.
  - verify: `TestSteer_ConverseFrameRoundTrip`
- AC5.2: `ServerCapabilities.steer` advertises the complete multimodal steer contract when runtime-enabled and false when disabled. The bit is computed **once** in composition (`Service.capabilities()`) and remains consistent across the CreateSession echo and rehydrated Session snapshot.
  - verify: `TestSteer_CapabilityAdvertised`; `TestSteer_CapabilitySingleSource`
- AC5.3: `task generate` keeps `contracts/gen` in sync; the new oneof arm does not change the behaviour of existing arms.
  - verify: inspection — `buf generate` output committed; existing Converse control frames unchanged.

---

### Scenario 6 — Composition gate + mecatui flip

The feature is **posture/run-level, default-on** (settled in #512): the engine steer inbox is armed by a config/posture knob resolved in composition ([`internal/app/posture.go`](../../internal/app/posture.go) — the `strict < trusted < auto < yolo` ladder), and the TUI flips between engine-steer (when the capability is advertised) and its existing local merge-queue (when not). The posture-→knob coupling lives in composition only ([`AGENTS.md` — the posture ladder](../../AGENTS.md)). The user sees the pending merged message as one thing being sent; `↑` edits the combined queued message (client-side merge).

The client-side merge and the engine-side slot are **distinct mechanisms that must not be conflated**: the TUI merges staged lines into one bundle *before* sending (one `message_id`), so the engine only ever sees one frame at a time; editing an already-sent-but-un-drained steer is **cancel-then-recompose** — `↑` issues a `steer_cancel` for the outstanding bundle, pulls the whole not-yet-drained set into the input as ONE editable blob, and resends as ONE replacement bundle with a fresh id. A drained `message_id` is **burned**: editing after the drain echo is a fresh draft (a new submission).

**Work:**
- composition (`internal/app` / `cmd/*`): the steer enable knob wired through `app.Build` into the engine deps + the `ServerCapabilities` bit; default on.
- `cmd/mecatui`: reads `steer`; when true, `enter` mid-run sends a native multimodal frame and renders its acked lifecycle. When runtime-disabled, the #228 local queue owns all mid-run text and media.

**Acceptance:**
- AC6.1: With steer enabled (default), the capability is advertised and a mid-run input reaches the model without a separate follow-up run.
  - verify: `TestSteer_EnabledByDefaultEndToEnd`
- AC6.2: With steer disabled, the engine inbox is inert and the TUI falls back to the client-side terminal queue unchanged.
  - verify: `TestSteer_RuntimeDisabledFallsBackToLocalQueue`
- AC6.3: The TUI renders the merged pending message as a single item, reflects the echoed/acked state honestly (queued-until-landed: a pending card at the bottom until the `EvSteer` echo lands it in context at its true position; sent vs promoted-after-race), and `↑` cancels the outstanding bundle and pulls the whole not-yet-drained set back as ONE editable blob (cancel-then-recompose, fresh `message_id` on resend; a late `none_pending` ack means the drain won — the steer shipped).
  - verify: `TestSteer_TUIRendersAuthoritativeState`; `TestSteer_CardGolden`; `TestSteer_TUIQueuedUntilLanded`; `TestSteer_TUIEditCancelThenRecompose`; `TestSteer_TUIEditBackNoDuplicate`
- AC6.4: `go run ./cmd/mecademo` still prints a full offline session (no behavioural regression with steer unused).
  - verify: demonstration — `go run ./cmd/mecademo`

---

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| HTTP/SSE steer endpoint (`POST /v1/sessions/{id}/steer`) | follow-up | deferred in #512; gRPC-only v1 — must be noted in the PR + ADR |
| ACP steer (`mecated acp`) | follow-up | ACP's `session/prompt` blocks one-in-flight with no mid-run client→server channel (`internal/adapter/acp/agent.go`), so ACP editors cannot steer in v1 — named here so the "gRPC-only" cut isn't read as covering all clients; must be noted in the PR + ADR |
| Steer-to-child (subagent / team member / parallel branch) | follow-up | needs a richer parent→child input channel than `CancelChild`; settled in #512 |
| Interrupt verb (abort in-flight response + replay) | not planned | steer-only; breaks no-replay-after-first-chunk |
| Intent-vs-action timing (record when a steer was *submitted* vs *drained*) | future enhancement | filed from #512; persist later, not this plan |
| Multiple distinct pending steers (a real list, not a single slot) | not planned | single append-default bundle; merge is engine-side by append (client-side: the ordered per-fragment queue) |
| Per-model steer disable | only if it proves needed | the gate is posture/run-level; a per-model dimension is a possible follow-up |

## Cross-cutting deliverables

- **ADR-0228** landing with this plan — the steer contract **as shipped** (rounds 2–3 rework applied): engine append-default single-slot inbox, watermark `message_id` correlation, the auto-promote-on-terminal-race rule + sequential active-run handoff, the clean-exit continue-run rule, the queue-only awaiting behaviour, and the gRPC-only-v1 / HTTP-deferred scope cut. Copy `docs/adr/template.md`.
- **`user-docs/reference/http-sse-api.md`** — a note that steer is gRPC-only in v1 (the HTTP run path has no mid-run client→server channel).
- **`docs/tui.md`** — the steer-mode queue card (pending / sent / promoted) and the capability-driven flip vs the local queue.
- **`docs/architecture.md`** — the steer inbox + Step 2a seam under the loop section (living "how it works").
- **`engine/CHANGELOG.md` + `engine/api/*.txt`** — the `Run.EnqueueSteer(text, parts)` signature, `Run.EnqueueSteerWithMessageID(text, parts, messageID)`, and `EvSteer` payload are exported engine-module surface, so `task api:update` is **required**, with compatibility notes in `engine/CHANGELOG.md`.

## Sequencing recommendation

Scenario 1 (engine inbox + boundary injection) is the foundation and lands first — everything else builds on the drain seam. Scenario 2 (the append-default / cancel contract) is the same `Run` structure and pairs naturally with 1. Scenario 3 (auto-promote) and Scenario 4 (awaiting) are independent service/loop behaviours that both depend only on 1. Scenario 5 (wire) depends on 1–2 and unblocks 6. Scenario 6 (composition + TUI) is last and the only client-facing piece. Orchestrate takes over at decomposition; the engine scenarios (1–4) parallelize cleanly once the seam exists.

## Named tests landing in this plan

`TestSteer_AcceptedOnEmpty`, `TestSteer_AppendAckAdvancesPhase`, `TestSteer_AppendedDrainMerged`, `TestSteer_AppendedMerges`, `TestSteer_AppendLinearizable`, `TestSteer_AskStillRequiresVerdict`, `TestSteer_AwaitingAskIsHeld`, `TestSteer_AwaitingResumeDrains`, `TestSteer_BurnedAckStillIgnored`, `TestSteer_CancelRetractsPending`, `TestSteer_CapabilityAdvertised`, `TestSteer_CapabilitySingleSource`, `TestSteer_CardGolden`, `TestSteer_CleanExitContinuesRun`, `TestSteer_ConverseCancelRetracts`, `TestSteer_ConverseFrameRoundTrip`, `TestSteer_RuntimeDisabledFallsBackToLocalQueue`, `TestSteer_DrainEmitsCommittedEcho`, `TestSteer_EmptyInboxNoOp`, `TestSteer_IdLessEchoClearsQueue`, `TestSteer_InboxConcurrentSafe`, `TestSteer_IngressUTF8Repaired`, `TestSteer_InjectedAtTurnBoundary`, `TestSteer_KeepsPromptPrefixByteStable`, `TestSteer_LiveRunEnqueues`, `TestSteer_MergeThreeIntoOneDrain`, `TestSteer_MessageIdRoundTrip`, `TestSteer_NeverClosedParked`, `TestSteer_NoSupersedePath`, `TestSteer_OutcomeMatrix`, `TestSteer_PendingSteerLostOnRestart`, `TestSteer_PreservesToolPairing`, `TestSteer_PromotedRelaySequential`, `TestSteer_PromotionUsesRunEntryFunnel`, `TestSteer_ProtoEnumAppend`, `TestSteer_RecomposedFragmentRendersPerPart`, `TestSteer_RecordedAndRehydrated`, `TestSteer_SurvivesCompactionBoundary`, `TestSteer_TerminalRacePromotes`, `TestSteer_TooLateNotDrained`, `TestSteer_TUIBurnedIdFreshDraft`, `TestSteer_TUIEditBackNoDuplicate`, `TestSteer_TUIEditCancelThenRecompose`, `TestSteer_TUIIgnoresStaleAck`, `TestSteer_TUIQueuedUntilLanded`, `TestSteer_WatermarkSplitOnQueuedAck`, `TestSteerMessageIDIsAtomicWithDrainedBundle`, `TestInvariant_recorded_streamed_model_view`.

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` — configuration reference regenerated and the matlatl strict link gate green.
3. `task generate` — `contracts/gen` regenerated from the proto change and committed.
4. `task api:update` was run for `Run.EnqueueSteer(text, parts)`, `Run.EnqueueSteerWithMessageID(text, parts, messageID)`, and the `EvSteer` payload; the regenerated `engine/api/*.txt` and compatibility notes are present.
5. `task ac-trace-strict` — every AC's `verify:` proof resolves (this plan is `landed`).
6. The named tests above are green and grep-locatable by their identifiers.
7. `go run ./cmd/mecademo` still prints a full offline session.
8. ADR-0228 lands; the HTTP/SSE deferral is noted in the PR body and ADR.

## Deferred decisions and known risks

- **The drain-observability race.** The client cannot observe the exact drain moment, so it renders the authoritative `EvSteer` echo (correlated by `message_id`); the ack reports `accepted` / `appended` / `too_late` / `none_pending` honestly. Resolved by Scenario 2's contract (engine authoritative, client renders echoed truth, id-correlated) — the residual UX polish (the "sending… vs sent" affordance) is scenario 6.
- **HTTP/SSE gap.** v1 is gRPC-only; an HTTP client cannot steer (no mid-run channel). Documented deferral; the unary `POST .../steer` endpoint is the cheap follow-up shape (mirrors `approve`/`cancel`).
- **Child steer.** Not built; requires a parent→child input channel. A reader must not assume steer reaches children — see *Out of scope*.
- **Prompt-cache.** The append-after-settle design keeps the prefix byte-stable, so the only cost is normal history growth (AC1.4 pins this). If a future variant ever inserts mid-history, the cache cost must be re-evaluated.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is satisfied.
