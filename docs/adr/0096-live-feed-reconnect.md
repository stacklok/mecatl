# ADR 0096 — mecatui live-feed reconnect: client-owned backoff + durable catch-up, no server cursor

- Status: Accepted
- Date: 2026-08-05
- Scope: `cmd/mecatui/client`, `cmd/mecatui/ui`
- Supersedes: none (refines the reconnect *mechanics* sketched in [ADR 0075](./0075-fire-result-delivery.md) decision 5; the delivery-channel decisions 1–4 stand)

## Context

mecatui opens a per-session `StreamSessionLive` subscription so a scheduled fire's
result is reported back into the conversation that created it (ADR 0075 Scenario 5).
When that stream closed or errored, `updateLiveMsg` cleared the channel refs and
returned — **no reconnect, no signal to the operator.** After a server restart, a
transient gRPC failure, a proxy idle timeout, or a network interruption, the UI
looked healthy while its notification path was dead; a correctly-persisted fire
result could be missed until the operator manually reloaded the session (issue #387).

The fix needed three properties: the feed must re-open automatically; the reconnect
must not hot-loop or thunder-herd the server; and a fire-result delivery note
emitted **during the gap** must be recovered and rendered **exactly once**.

The load-bearing scoping decision was *how to catch up*. The heavyweight option
(considered in the architect plan) was a server-side durable cursor: widen
`port.EventLog` with `ReadFrom(ctx, id, fromSeq)` + `Append` returning an ordinal,
add a `from_seq` field to `StreamSessionEventsRequest` and a `log_seq` field to the
wire `Event`, and stamp a durable-log append ordinal on every relayed event so the
client could resume mid-log and dedup on that ordinal. That is an engine-port +
proto (wire-contract) change to recover at most a handful of buffered events.

## Decision

Own the reconnect entirely on the **client**, and catch up via the **existing
full-replay** seam — no engine-port or proto change.

- **Reconnect loop in `cmd/mecatui/client`** (`ReconnectLiveCmd` /
  `reconnectLiveLoop`): on a live-feed `StreamClosedMsg`/`StreamErrMsg`, the ui
  drives a bounded exponential-backoff loop (base 500ms, ×2 per attempt, cap 30s,
  ±20% jitter) that re-opens `StreamSessionLive`. The loop stops on ctx
  cancellation (session switch / TUI exit). It emits `LiveReconnectingMsg`
  (degraded footer state) per attempt and `LiveReconnectedMsg` on success.
- **Catch-up = the existing durable replay.** Before each live re-open, the loop
  drains `StreamSessionEvents` (the cloud-native Phase-3a read-back, the SAME feed
  the `/sessions` transcript viewer uses) once, recovering any delivery note emitted
  during the gap. **No `from_seq` cursor, no `log_seq` ordinal, no `port.EventLog`
  widening** — the full log is replayed and the catch-up events flow through the
  SAME `updateStreamEvent` projection as a live event, so a `DeliveryNoteMsg` caught
  up here renders identically to a live one.
- **Exactly-once is a client-side FireID dedup, not a server ordinal.** The number
  of events the gap can contain is tiny (the live subscriber channel is 64-buffered;
  a disconnect gap is a handful of events), so replaying the full log is cheap and
  bounded in practice. The ui keeps a per-session `seenFireIDs` set keyed on
  `DeliveryNoteMsg.FireID` (the scheduler's fire id, stable across replay and live);
  a note arriving via BOTH catch-up and the re-opened live feed renders once. A note
  with an empty FireID (malformed — `deliverNoteFrom` could not parse it) is NOT
  deduped. The set resets on session create/switch.
- **The fenced delivery discriminator is unchanged.** A plain user prompt (never
  fenced) cannot be misclassified as a delivery note; a regression test pins this.
- **The degraded state is a concise footer cue**, not a new UI phase: "live feed
  reconnecting (attempt N)…" while reconnecting, cleared on reconnect.
- **The generation-guard discipline is preserved.** The reconnect loop gets its own
  `liveReconGen` (parallel to `liveGen`) so a stale reconnect reader after a session
  switch is dropped without triggering a reconnect for the old session; a second
  reconnect for the same session is refused (no duplicate concurrent subscriptions).

## Consequences

**Easier:** the feed self-heals with no operator action; missed deliveries recover;
the change is confined to `cmd/mecatui` (no engine-port stability review, no
`task api:update`, no proto regen, no `task generate`).

**Harder / accepted costs:**
- The catch-up **reads** the full event log per reconnect (O(log), not O(gap)) — but
  it **forwards only delivery notes** (`catchUpReplay` drops every non-`DeliveryNoteMsg`
  event), so the visible transcript is never re-appended. Replaying the whole log into
  the live conversation would re-render the entire prior history on every blip (user
  prompts, assistant text, tool calls); scoping the forwarder to delivery notes is what
  keeps the catch-up honest. If a future deployment shows the full-log *read* is hot,
  the server-side `from_seq` cursor is the documented upgrade path — deferred, not
  rejected.
- The catch-up→live handover has a narrow window: an event published between the
  replay iterator exhausting and the live `Subscribe` registering is missed *this*
  reconnect cycle and recovered on the next (at-least-once, eventually), because the
  live `Subscribe` retains no pre-subscribe events. Accepted for the lean design; the
  `from_seq`/`log_seq` cursor closes it exactly.
- The reconnect loop is a long-lived goroutine + channel per active session while
  degraded; it is bounded (stops on ctx cancel / reconnect success) and rides the
  existing live-feed lifecycle (torn down in `disarmLiveFeed` / `resetSession`).
- `seenFireIDs` accumulates one entry per delivered fire for the session's lifetime
  (cleared on `resetSession`, not on reconnect — correct, since fire ids are unique
  per session and must suppress across reconnects). Fire ids are small, so growth is
  bounded by schedule cadence × session lifetime.

## See also

- [ADR 0075 — Fire-result delivery](./0075-fire-result-delivery.md) (decision 5, the
  delivery channel this hardens; the reconnect mechanics here refine its
  "reconnecting TUI replays missed deliveries" note).
- [ADR 0056 — MCP client reconnect](./0056-mcp-client-reconnect.md) (the same
  client-owns-the-reconnect boundary, applied to the MCP transport).
- [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) — the live-feed reconnect mechanics.
- The lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
