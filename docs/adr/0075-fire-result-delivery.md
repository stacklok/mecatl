# ADR 0075 — Fire-result delivery: a scheduled fire reports back into the originating chat

- Status: Accepted
- Date: 2026-07-27
- Scope: the scheduled-tasks feature's delivery channel — routing a fire's result back into the conversation that created it ("remind me of X", "ping me when Y", "monitor this PR every 30m"), closing ADR 0073's explicitly-deferred follow-up
- Supersedes: [ADR 0059](./0059-scheduled-tasks.md) — **decision #8's "pull-only delivery v1" posture only.** The durable registry, tick loop, claim-before-fire, fresh-session-per-fire, posture-pinning, and the fenced-untrusted carried-context rendering all stand.
- Superseded by: none

## Context

ADR 0059 (decision #8) shipped **pull-only** delivery: a fire mints a fresh
`sched--` session and ends there; the result is pulled via
`GetFire`/`ListFires`/the mecatui overlay. ADR 0073 kept that posture and named
routing the result **back into the originating conversation** the "ping me when
X" loop — a follow-up concern, deliberately out of scope.

That follow-up is the actual product. The scheduled-tasks UX is incomplete
without it: the whole point of the in-chat `Schedule` tool (ADR 0073 decision
#1) is that the human asks *in the conversation* — "remind me of X", "ping me
when Y", "monitor this PR every 30 minutes" — and expects the answer **in the
same conversation**. A fire whose result lives only in a separate, hard-to-find
`sched--` session forces the developer to go hunting for the outcome, which is
worse than the `cron` the feature set out to replace. The forces at play:

1. **Delivery into the origin chat is the UX the tool promises.** The model
   registers a task mid-conversation; the human steers it in the same chat. The
   result must arrive there too — pull-only delivery is the "horrible UX" gap,
   not a feature.

2. **The fire's result is UNTRUSTED content entering a live conversation.** A
   scheduled fire runs unattended, may itself have been prompt-injected, and its
   output is model-authored + tool-result-laden. Delivering it as a *live
   instruction* into the origin chat would carry injection across the boundary.
   The trust shape is identical to carried context (ADR 0059 Phase 2), which is
   already solved: render as a **fenced untrusted** preamble
   (`agent.FenceUntrusted` + `NeutraliseFraming`), never as live instructions.

3. **The origin session may be busy when the fire completes.** A fire fires on
   the scheduler's clock, not the conversation's. The origin session may be
   mid-run (a turn in flight), completed, cancelled, failed, or awaiting an
   approval. A synchronous "start a run on the origin session now" collides with
   the single-writer run-entry discipline (`runEntryMu` + the session lease) and
   the state machine.

4. **The run-entry funnel already reopens terminal sessions.** The service's
   `StartRunContent` → `loadAndReopen` path already drives a
   completed→Reopen / cancelled→Interrupt / failed→Recover session back to idle
   before recording a prompt (issue #51). Delivery reuses it; it is not a new
   reentry mechanism.

The design resolves these into four decisions, recorded here as a frozen record;
current behaviour lives in `docs/architecture.md`.

## Decision

1. **A schedule records its origin conversation at create-time.** The
   `Schedule` tool's `create` verb captures the calling session's id onto a new
   `ScheduleSpec.OriginSessionID` field (empty for an out-of-band API/CLI create
   with no conversation). **The capture seam is new plumbing:** a catalog tool's
   `Execute` receives only `(ctx, call, ws)` — no session id — so composition
   binds the per-session id into the tool's `ScheduleManager` closure at catalog
   assembly (the per-session engine factory already builds a catalog per
   session). It is NOT a ctx-value and NOT a `parentCaps` widening (layering /
   fragility), and the model cannot supply or influence it (no `origin` arg on
   the tool schema) — delivery is always to the creating session. The field rides
   the SAME validated create-seam (`validateScheduleSpec`) — a non-empty
   `OriginSessionID` must reference an existing session, fail-closed. The origin
   id is **metadata**, never rendered into a prompt, so it carries no injection
   weight.

2. **Delivery is a fenced-untrusted harness note recorded into the origin
   conversation, NOT a live instruction and NOT a new event family.** When a
   fire of a schedule with a non-empty `OriginSessionID` reaches its terminal
   `EvResult`, the fire path renders the outcome (schedule name, fire id, stop
   reason, and the fire's final text — clamped, `NeutraliseFraming`'d) as a
   **fenced untrusted** block and enqueues it as a *pending delivery* for the
   origin session. It rides the EXISTING notice-channel precedent (the
   background-subagent completion notice, `injectBackgroundNotice`): the note is
   recorded as **ordinary harness-framed user history** at a turn boundary —
   provider-legal (history there always ends on a user prompt / tool result /
   nudge, never inside a `tool_use` pair), replay-clean, compaction-safe. **No
   new `session.Event` type, no proto change** — the delivery is recorded
   history, not a streamed event.

3. **Delivery is asynchronous and state-aware via a DURABLE per-session
   pending-delivery queue + drain.** The enqueued note is drained into the origin
   conversation at the **next run-entry boundary** for that session, recorded at
   the loop's **Step 2a, BEFORE `BeginTurn`** (the `injectBackgroundNotice`
   seam), where history always ends on a user prompt / tool result / nudge —
   never inside a `tool_use` pair — so the note is provider-legal and never
   orphans a tool call. The queue is keyed on the **SESSION**, not the per-Run
   `noticed`/`delivered` registry (which dies with its run), and is
   **persist-in-snapshot** (an ADR 0027 List 1 + List 2 row) so a restarted
   process drains still-pending notes rather than losing them. State handling:
   an idle/completed origin is delivered on the next turn (the fire path drives
   a delivery run through `StartRunContent`, which reopens-if-completed); a busy
   origin accumulates pending deliveries drained at its turn boundary; a
   cancelled/failed origin is recovered by the same `loadAndReopen` funnel; an
   **awaiting** origin is NOT driven past its pending ask (the note is queued and
   delivered after the ask resolves via `resumeFromAwaiting`). The session lease
   is process-held for the session's life, so a single-process delivery
   short-circuits `heldLeases[id]` (no collision), while a DIFFERENT replica's
   fire cannot deliver into an origin another process owns — it queues + WARNs
   and the owning replica's next run-entry drains (single-writer is
   intentional). A **deleted, collected-child, or `sched--` fire** origin
   degrades to pull-only with a WARN (never fails the fire, never a delivery
   loop), and the result remains pull-able via `GetFire`/`ListFires`.

4. **The fire itself is unchanged — delivery is a side-channel, never a
   failure mode.** The fire still mints its own fresh `sched--` session and ends
   there (ADR 0059 decision #7 stands). Delivery happens AFTER the fire's
   terminal result, is decoupled from the fire's success, and a delivery error
   (origin busy-then-deleted, a drain failure) WARNs and never re-fires nor
   fails the already-recorded fire. The carried-context toggle
   (`CarryContext`) and delivery are orthogonal: carried context flows
   forward (prior fire → next fire), delivery flows back (fire → origin chat).

5. **Delivery is VISIBLE in the connected client, not merely durable — the TUI
   surfaces it live.** A delivered-but-invisible note is the "horrible UX" this
   ADR exists to kill: the mecatui is a thin relay with no server→client push
   channel, rendering only the events of a `Converse` stream it opened, so a
   server-initiated delivery run has no client draining it and never reaches the
   screen. Because mecatui hosts mecated **in the same process** (the embedded
   server), the scheduler, the origin session, and the TUI share a process — so
   the delivery run's events are published onto a **per-session live event
   subscription** the connected TUI holds open (the `relayEventsSSE` merged-stream
   precedent), and the TUI renders them through the SAME `EventToMsg` projection
   a user-initiated run uses (projection equivalence). The delivered note renders
   as a **distinct delivery card** (a scheduled-task affordance + the schedule
   name), NOT as a user-typed prompt and NOT silently as the model's own text, so
   the operator can tell provenance at a glance. A reconnecting TUI replays missed
   deliveries via the existing `StreamSessionEvents` replay feed (which relays
   `EvUserPrompt`). This is not an optional polish layer: an engine-only land
   (durable but invisible) does NOT satisfy the feature. Delivery surfaces at a
   turn boundary / as a new delivery run — never a mid-stream injection into an
   in-flight turn. The embedded in-process path is the ship-gate; extending the
   SAME live card to a REMOTE mecated rides a per-session server-streaming
   subscription RPC (the wire analogue — a proto + `task generate` change,
   relaying the delivery's `EvUserPrompt` on the live wire) and is sequenced as
   the final wave: additive to a working embedded product, not a blocker for it.

## Consequences

- **The in-chat scheduling loop closes — and is SEEN.** "Remind me", "ping me",
  "monitor this" report back into the same conversation, rendered live in the
  operator's mecatui as a delivery card. The separate `sched--` session still
  exists (the fire's full transcript, GC-retained), but the human no longer has
  to find it — the outcome arrives, visibly, where the task was registered.

- **Pull-only stays the floor, not the ceiling.** `GetFire`/`ListFires`/the
  mecatui overlay are unaffected; an out-of-band schedule (empty
  `OriginSessionID`) behaves exactly as before. Delivery is additive.

- **The trust boundary holds by construction.** The delivered note is
  fenced-untrusted recorded history — the model in the origin chat *reads* it as
  context, it is never replayed as a live instruction, and a forged fence or
  harness header inside the fire's text is neutralised. The origin id and the
  fire id are the only metadata, and neither is model-authored.

- **The mechanism is two reused precedents, not new infrastructure.** The
  create-time origin capture reuses the validated create-seam; the delivery
  drain reuses the background-completion notice channel + the run-entry reopen
  funnel. The genuinely new surface is one `ScheduleSpec` field, a pending-
  delivery queue, and the drain hook in the loop — all composition/loop, no new
  ports, no proto, no wire change.

- **A delivery is best-effort, honestly.** If the origin is gone, the result is
  not lost — it stays pull-able. The fire's success is never hostage to the
  origin conversation's availability.

## See also

- [ADR 0073 — Schedule tool](./0073-schedule-tool.md) — the in-chat surface
  whose delivery gap this closes (its "ping me when X" follow-up).
- [ADR 0059 — Scheduled tasks](./0059-scheduled-tasks.md) — the substrate
  (registry, tick, claim-before-fire, fire model, fenced carried context) this
  amends decision #8 of.
- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md) — the run-entry funnel
  (`loadAndReopen`, Reopen/Interrupt/Recover) delivery reuses.
- [`docs/architecture.md`](../architecture.md) — the living reference.
