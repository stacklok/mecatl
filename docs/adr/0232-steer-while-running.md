# ADR 0232 — Steer-while-running: inject a user message into an in-flight run

- Status: Accepted
- Date: 2026-08-13
- Scope: the agent loop (`engine/agent`), the gRPC `Converse` wire, the service/composition layer, and mecatui.

## Context

mecatl's queued follow-ups (issue #228) are **client-side and turn-terminal**: a
line typed while a run streams is staged in the TUI and submitted as a brand-new
follow-up run only when the current run ends. A long multi-tool run cannot be
nudged mid-flight. Claude Code's "steer while running" folds queued input into the
*running* turn: the message is injected as a user message between the settled tool
results and the next model replay, so the agent course-corrects without a separate
turn (issue #512).

The load-bearing enabler: mecatl's LLM adapters are **stateless** (`store:false`,
full replay each turn). Injecting a steer is appending a `Message{Role: user}` to
the history before the next replay — **no provider API support is required**, and
the feature is portable across Anthropic Messages / OpenAI Responses / Chat
Completions. The whole feature is a harness/loop concern.

The subtle risks, called out in #512: the supersede/drain race (the client cannot
observe the exact drain moment across stream latency), the lost terminal race (a
steer arriving after the run went terminal), and the recorded == streamed ==
model-view invariant.

## Decision

- **Steer-only, after-settle, never interrupt.** The steer takes effect at a turn
  boundary *after* the current streamed response and its tool batch settle. It
  never preempts or aborts an in-flight model call (that would break
  no-replay-after-first-chunk and force retraction of already-emitted events).
- **Append-after-settle, never insert-mid-history.** The steer is appended *after*
  the just-finished tool results as a trailing user message, so the byte-stable
  prompt prefix stays a valid cache prefix and the steer pays no prompt-cache
  penalty beyond normal history growth.
- **A `Run`-scoped, single-slot, append-default mutex inbox**
  (`engine/agent/steer.go`). At most one pending steer bundle per run; a second
  `EnqueueSteer` on the occupied slot **APPENDS** — `pending += "\n\n" + text`,
  outcome `SteerAppended` (supersede and slot_full were dropped in the round-3
  rework — a channel can't linearize the replace/close, and a silent replace is a
  lost user message): replacing a pending steer is an explicit **cancel-then-resend**
  (`CancelSteer`, then a fresh steer with a fresh `message_id`). The state is a
  mutex-guarded `{closed, pending, has}` triple — every transition is ONE critical
  section, so the observe-then-replace race is impossible by construction.
  `CancelSteer` retracts; the boundary drain commits. The inbox is in-memory and
  **best-effort**: a pending (un-drained) steer is lost with the run on a crash —
  the explicit honest contract, not persisted across restart (reset-by-design;
  inventoried in [ADR 0027](./0027-cloud-native.md) List 1 row 60 / List 2 row 37).
  Steer text is repaired to valid UTF-8 at ingress (`session.ToValidUTF8`), so
  recorded history, the echo, and the model view stay byte-identical.
- **Drained at the Step 2a turn-boundary seam** in `runLoop` (the same
  provider-legal point `injectBackgroundNotice`/`drainPendingDelivery` use),
  recorded via `recordContinuation` (`RecordUserPrompt` + the log-only
  `EvUserPrompt`), so it replays cleanly, survives `session.ValidateToolPairing`
  and compaction, and rehydrates under [ADR 0038](./0038-event-sourced-rehydration.md).
- **The engine is authoritative; the client renders the echoed truth.** The drain
  emits `EvSteer` carrying the committed text; the enqueue/cancel outcome is a
  closed enum (`accepted`/`appended`/`retracted`/`none_pending`/`too_late`), not
  booleans.
- **Client-minted `message_id` correlation (watermark).** Every `Steer` frame
  carries a unique client-minted `message_id`; its `steer.outcome` ack echoes
  the frame's own id verbatim. On the `EvSteer` drain echo the wire carries a
  **watermark** — the LATEST (tail) contributing send's id of the bundle that
  just drained — and the client splits its ordered queue on it (sends up to and
  including the watermark drained; sends after it still pending). The client
  ignores a stale ack (one whose id it no longer holds in the queue).
  Correlation is positional, never text-match. The engine inbox parks text only,
  so the id lives at the Service's per-session FIFO (`trackSteerMessageID` /
  `LookupSteerMessageID` / `dropSteerMessageID`) of the ordered frame ids. Ids
  are clamped to a 64-rune prefix at track (CWE-770).
  **Fragility (recorded deliberately).** The text-only-engine + Service-FIFO split
  correlates the echo's id **positionally** (the TAIL of the session's ordered
  id-list for the bundle), *never* by text-match — sound today because the TUI's
  one-bundle-outstanding invariant makes send order == drain order. It silently
  breaks (echoes `""`) if a future sender stops batching, or if a second sender
  surface (e.g. the deferred HTTP/SSE steer endpoint) lands. When that happens,
  move the id into the engine inbox (`steerInbox.pending` as a `{text, id}` pair)
  rather than growing a heuristic at the wire layer;
  `TestLookupSteerMessageIDExactUnderDuplicateTexts` (in
  `internal/adapter/server/steer_watermark_pin_test.go`) is the pin this
  paragraph promises — identical-text sends MUST return the tail id.
- **The clean-exit continue-run rule.** A would-be clean end (meaningful text,
  benign stop) while a steer is still parked does NOT terminate: `finishTurnNoTools`
  re-enters the loop so the very next Step 2a drains the steer and the turn it
  feeds consumes it. The never-drop contract stays engine-internal — the run
  extends (bounded by `Limits.MaxTurns`), no promote, no client handoff, no
  injected nudge text. The terminate paths drain-then-close
  (`closeSteerDrained`): a steer parked when the run ends for another reason is
  recorded into durable history first, never closed unconsumed.
- **Lost terminal race → auto-promote + sequential active-run handoff.** A steer
  arriving for a session whose run is already terminal is promoted to a fresh
  follow-up run through the existing hardened run-entry funnel
  (`StartRunContent`/`loadAndReopen` + lease + recover-if-terminal) — never
  silently dropped. Promotion reuses the funnel; it never starts a run on an
  unrepaired terminal session. The promote path alone awaits the original run's
  deregistration, bounded by the promote-grace (`steerPromoteGrace`): the
  just-terminal run's relay may still be draining (still registered), and only a
  terminal-but-still-registered run can deregister inside the grace — a run still
  registered at the lapse is genuinely in-flight, so the promotion is refused.
  The promoted run is handed off **sequentially on the same Converse stream**:
  the RPC relays the original run, then relays each promoted run in turn before
  returning — one relay owner at a time (`sendErr` single-owner, every `Send`
  across the one `streamSender` mutex), the control target (`ResumeApproval` /
  `Cancel` / `CancelChild`) swaps to the promoted run atomically before its relay
  starts, and the promoted run is `FinishRun`-deregistered before the RPC returns
  (its terminal outcome is reported inline as the `steer.outcome` ack with
  `promoted=true` — never an orphaned relay, never an ack after close).
- **Awaiting-ask steer is queue-only.** While a run is parked `awaiting` on a
  permission/plan ask, the loop is suspended and the steer is held, drained at the
  resumed run's first boundary after the verdict. The ask still requires an
  explicit verdict; the steer is purely additive ("yes-and"/"no-and").
- **gRPC-only v1.** The steer rides a new `steer`/`steer_cancel` oneof arm on the
  existing bidi `Converse` stream, plus a `ServerCapabilities.steer` bit (additive
  grow) and the `EvSteer` echo. **HTTP/SSE and ACP steer are deferred** — the HTTP
  run path (`POST /v1/sessions/{id}/prompt`) and ACP's blocking `session/prompt`
  have no client→server mid-run channel; a unary `POST .../steer` (mirroring
  `approve`/`cancel`) is the cheap follow-up shape. This deferral is recorded in
  the PR body.
- **Posture/run-level gate, default-on, posture-independent.** `Config.DisableSteer`
  (opt-out) → `Deps.EnableSteer` → `ServerCapabilities.steer`; the capability bit
  is computed once in composition. mecatui flips between engine-steer (capability
  present) and the #228 local merge-queue (absent) on it.
- **Queued-until-landed rendering.** The client renders a sent-but-un-drained
  steer as a PENDING card at the bottom of the transcript; when the `EvSteer` echo
  lands, the card clears and the message appears IN CONTEXT at its true position
  (the echo is stream-positioned — emitted after the settled tool results, before
  the next `turn.start`). The client never infers position by text-matching
  history; the `message_id` tells it which in-flight send the echo commits.
- **Client edit = cancel-then-recompose.** mecatui keeps an ordered queue of
  sends (id + fragment per `enter`); `↑` issues a `steer_cancel` for the
  outstanding bundle (the watermark id), pulls the pending sends into the input
  as ONE editable blob, and resends as a fresh fragment under a NEW
  `message_id` (already-drained sends are never re-sent — the watermark split
  keeps the queue honest). The late ack reconciles the race: `retracted` =
  clean edit, `none_pending` = the drain won (the steer shipped as sent).
- **Main-run only.** Steer-to-child (subagent/team/parallel) requires a richer
  parent→child input channel than `CancelChild` and is deferred.
- **All Converse-stream `Send`s are serialized** behind a single-writer gate, and
  the wire routing has one owner (`Service.Steer`/`Service.CancelSteer`); the gRPC
  handler is a dumb frame→Service mapper (panel-review repair).

## Consequences

- A running agent can be steered mid-flight across every provider, with no API
  change — closing the #228 gap between terminal-queue and boundary-injection.
- The client contract is honest about the race: the drain echo, the outcome enum,
  and the `message_id` correlation make the committed text observable rather than
  assumed — and an occupied inbox merges (append) while replacement is an
  explicit cancel-then-resend, never a silent replace.
- The terminate-window steer reliably promotes (the promote path awaits the
  original run's deregistration, bounded by the promote-grace) instead of
  dropping, and the promoted run relays sequentially on the same stream before the
  RPC returns — the client never has to orchestrate a second stream, and the
  control surface follows the active run.
- The steer genuinely steers mid-task: the clean-exit continue-run rule keeps a
  live run from ending while a steer is parked, so an accepted steer is never
  dropped engine-internally.
- The promoted follow-up run rides the Converse stream's handler ctx: a client
  that disconnects mid-promotion cancels the follow-up (parity with a prompt's
  own client-bound lifetime — `drainChildren` + `FinishRun` persist the cancelled
  run, so nothing leaks). A client holding the stream for the *original* run sees
  a shorter window for the promoted one; noted as accepted for v1.
- **Costs:** a steer grows history (re-sent every subsequent turn, like any user
  message); the TUI maintains a second (server-authoritative) mid-run input state
  machine alongside the local queue; HTTP/SSE and ACP clients cannot steer in v1
  (documented deferral).
- Child runs arm an inbox no wire path can reach (steer-to-child deferred) —
  harmless, enforced implicitly by registration.

## See also

- The acceptance plan + verification contract: [steer-while-running](../acceptance/steer-while-running.md).
- The loop seam: [the agent loop](../architecture/agent-loop.md) § Steer-while-running;
  the run-entry funnel and the recorded == streamed == model-view invariant in `AGENTS.md`.
- [ADR 0038](./0038-event-sourced-rehydration.md) (event-sourced rehydration),
  [ADR 0069](./0069-plan-approval-gate.md) (the awaiting seam), and the lifecycle
  convention in [ADR 0002](./0002-documentation-lifecycle.md).
