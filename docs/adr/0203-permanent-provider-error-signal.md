# ADR 0203 — Neutral permanent-provider-error signal

- Status: Superseded
- Date: 2026-08-05
- Scope: `engine/port.PermanentError` interface → `session.ResultPayload.Permanent` → `EvRecoverNotice` advisory
- Superseded by: [ADR 0239](./0239-semantic-stream-retry.md) for typed retry semantics

## Context

When the LLM provider returns an error that is a *permanent client-side rejection* — an
invalid API key, a content-policy block, a context-window overflow — retrying the
identical request cannot succeed. Before this ADR, every provider error was rendered
identically: a terminal `StopError` that `Recover` would reopen into an idle session.
The operator (or an automated retry loop) would hit "retry," burn another provider call,
and get the same error. Worse, `Recover` is *honest* — it makes retry possible, not
guaranteed — so refusing to recover a `StateFailed` session would break the contract and
strand genuinely transient failures (5xx, rate limits).

The gap was threefold:

1. **No neutral signal.** The loop and the result event had no way to label a failure as
   "permanent." A raw error string is an opaque blob — a client or operator can't
   distinguish "retry may work" from "retry can't work" without parsing
   provider-specific text.
2. **No pre-retry advisory.** When `Recover` clears the `StateFailed` flag, the session
   is back to idle, ready to burn a provider call on the same unrecoverable request. The
   operator gets no warning before that call.
3. **The TUI had no structured rendering.** The user saw a raw error block, identical
   for transient and permanent failures. The only path to a non-raw presentation was the
   legacy `transientVocab` text-classifier heuristic, which misclassifies a permanent
   400 "invalid_encrypted_content" as transient.

Constraints that bound the decision:

- `port.LLMRequest` must NOT widen (provider-neutral DTO, guarded by
  `engine/port/llm_neutral_test.go`).
- `engine/agent` must stay provider-agnostic — no OpenAI/Anthropic type crosses into
  the loop.
- `Recover` stays honest and unblocked — a permanent failure still recovers, because
  a session-level gate ("never retry this session") would wrongly trap a genuine
  transient failure that happened to immediately follow a permanent one.
- Fail-open: an unclassifiable error is treated as NOT permanent, preserving today's
  behaviour.

## Decision

**Thread a neutral `port.PermanentError` interface through the error chain** — it
rides the error (reachable via `errors.As`), NOT `port.LLMRequest` — then carry the bit
to `session.ResultPayload.Permanent` on the terminal `EvResult`, persist it on the
snapshot, and emit a transient `EvRecoverNotice` at the run-entry funnel ONCE when a
permanently-failed session is recovered for re-entry.

The pieces:

1. **[`PermanentError` in `engine/port/llm.go` at the decision commit](https://github.com/stacklok/mecatl/blob/ad1cfe3c89a640905b88fb69f9905df498ba5c9c/engine/port/llm.go):** a new interface with a single `Permanent()
   bool` method, documented as fail-open (a nil target / non-implementing error is NOT
   permanent). It carries no provider detail — the adapter's `Error()` string is the
   human-readable surface.

2. **`engine/session/session.go` (`RecordFailurePermanence` / `FailurePermanence`):** a
   boolean on the `StateFailed` aggregate, set right after `Fail()`, cleared by
   `resetToIdle` (every transition out of `StateFailed`: Recover, Interrupt, Reopen). A
   healed session never keeps a stale permanence marker.

3. **`engine/adapter/sessnap/sessnap.go` (`Snapshot`):** the flag round-trips
   through the snapshot so it survives a process restart (`omitempty` — additive, no
   format-tag bump).

4. **`engine/session/event.go` (`ResultPayload`):** the terminal `EvResult`
   carries the bit. Meaningful ONLY when `Stop == StopError`.

5. **`engine/agent/loop.go` (`permanentCause`):** the `terminate` path threads
   `permanentCause(err)`, which `errors.As`-checks for `port.PermanentError`. The
   `terminateComplete` path (ChunkDone StopError — no Go error to classify) always
   passes `false` (honest fail-open).

6. **Provider adapters** (`provider/anthropic`, `provider/openai`, `provider/openaichat`):
   each adapter's stream error type implements `Permanent()`. The predicate:
   context-overflow-message OR (status ≠ 0 AND not retryable). Retryable = 408, 429, or
   5xx; everything else is permanent. The context-overflow discriminator is a
   keyword-based check (`"context window"`, `"context length"`, `"maximum context"`,
   `"exceeds the token limit"`, `"exceeded the token limit"`) duplicated across the
   three provider modules (separate Go modules — a shared dep is worse).

7. **`internal/adapter/llmresilience/llmresilience.go`:** wraps surfaced non-retryable
   errors in a `permanentError` shim at two sites: the establish path (a non-retryable
   classification before the first chunk) and the mid-stream error path (a non-retryable
   stream error that is not `context.Canceled`). Breaker-exhausted, idle-timeout, and
   cancellation errors are never wrapped.

8. **`internal/adapter/server/service.go` (`recoverNotices` + `RecoverNotice`):**
   `loadAndReopen` captures `FailurePermanence()` BEFORE `Recover()` clears it, stashes
   a once-per-recovery advisory text in a `sync.Map` keyed by session id. The relay
   adapters (gRPC `Converse`, HTTP `relayRunSSE`) call `RecoverNotice(id)` after
   `StartRunContent`, emit an `EvRecoverNotice` synthetic event BEFORE the main event
   loop, and the entry is consumed (returned once, then deleted). The notice is an
   advisory — it does NOT block the run.

9. **`engine/session/event.go` (`EvRecoverNotice`):** a new string-passthrough event type
   (like `EvNoProgress`), CLIENT-VISIBLE, rendered as a transient status/warning.

10. **TUI** (`cmd/mecatui`): `ResultMsg.Permanent` forces `Transient=false` (no
    auto-retry; the legacy `transientVocab` heuristic stays as the fallback for old
    servers — `TODO #346`). A `Permanent` StopError renders a ONE-LINE summary block
    (`✗ <first line, ≤120 runes> — retrying won't help; the request is rejected. Start a
    new session or /clear.`) with the raw payload behind `ctrl+t` expand under a dim
    `raw payload:` header. `RecoverNoticeMsg` renders as a transient warning status line.

### Rejected alternatives

- **A new `StopReason` variant (`StopPermanent`):** would break the closed-enum string
  passthrough (every consumer — proto, ACP, mecatui, schedule — must learn it). Would
  also force a NEW terminal state (not `StateFailed`), making `Recover` inapplicable
  and breaking the honest-recovery contract.
- **A recorded conversation note (model-visible bloat):** writing `[System: the last
  failure is permanent]` into the conversation history is the wrong channel — it travels
  with the persisted replay, costs tokens every turn, and the model can't act on it
  (it's an operator concern, not a model instruction).

## Consequences

- **Easy:** the operator or TUI now knows when retrying can't help, before burning a
  provider call.
- **Easy:** a headless deployment can inspect `Result.Permanent` and route to a dead-
  letter queue instead of an infinite retry loop.
- **Honest but unchanged:** `Recover` still unblocks the session. The advisory warns;
  the operator decides. A second prompt on the same session (not a retry of the same
  request) succeeds normally because the session recovered cleanly.
- **Cost:** a new interface on `engine/port` (minor API addition) and a new event type
  (string passthrough, no proto enum). Every provider adapter implements one method.
  The snapshot gains one `omitempty` boolean.

## See also

- [docs/design/IMPLEMENTATION-NOTES.md](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) — dense
  mechanics ("Permanent provider-error signal" section).
- [docs/architecture/agent-loop.md](../architecture/agent-loop.md) — the `result` event
  now carries `Permanent`.
- [docs/tui.md](../tui.md) — the one-line permanent-error rendering and
  `RecoverNoticeMsg` advisory line.
- [ADR 0027](./0027-cloud-native.md) — List 1/List 2 inventory rows for the
  `recoverNotices` map and the snapshot `permanent` flag.
---
