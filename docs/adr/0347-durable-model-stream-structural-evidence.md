# ADR 0347 — Durable model-stream structural evidence

- Status: Accepted
- Date: 2026-09-17
- Scope: bounded structural evidence for provider attempts in the target-bound session debugger
- Supersedes: ADR 0255 only where successful/incomplete attempt coverage is concerned
- Superseded by: —

## Context

ADR 0255 retains sanitized evidence for failed or suppressed provider attempts, but deliberately
retains no structural evidence for a successful HTTP/model stream. A stream can therefore return
HTTP 200, appear to finish, and produce anomalous assistant text without leaving durable evidence
of framing, accepted protocol events, terminal observation, or local completion state. Issue #1676
requires evidence for that diagnostic gap without retaining raw provider traffic or weakening the
existing debugger authority boundary.

## Decision

Extend the existing `network.attempt` payload with a small optional structural section for each outer resilience attempt/decision slot, including successful and incomplete streams. Reuse the existing `port.AttemptObserver`; `llmresilience` owns the final summary for the slot, while adapter-internal repair requests remain folded into that slot. The agent loop remains the sole event producer: it rejects invalid structural values and binds accepted facts to the trusted target, run, turn, and attempt identity. The server relay persists the debugger-only event through the existing EventLog path; adapters never write durable storage directly.

The structural vocabulary is deliberately small: `ProviderTerminalObserved *bool` and `StreamOutcome string`. The closed outcomes are `complete`, `incomplete`, `stream_error`, `cancelled`, and `unavailable`. A pointer is used so absent/unavailable observation does not collapse into a known false value. No accepted-chunk, response-byte, raw framing, or raw protocol-event count is retained in v1. Existing failure fields retain their ADR 0255 meaning; legacy and pre-stream rows lack only the new structural fields.

`ProviderTerminalObserved` means that a recognized provider protocol terminal semantic was accepted, not that the result was successful. OpenAI Responses, Chat Completions, and Anthropic Messages map their supported terminal semantics independently; unsupported facts remain unavailable. A missing terminal semantic is never fabricated. `complete` requires the adapter's normal terminal semantic and local normal completion; `incomplete` covers an explicit incomplete semantic or an ended stream without the required terminal; `stream_error` covers a recognized provider error or stream/decoder/transport failure; `cancelled` is used only when cancellation is trustworthy; `unavailable` means an entered stream had no valid structural support.

EventLog append order is the durable row identity. Existing run/turn/attempt values correlate observations but are not globally unique across process restarts. The target-bound debugger is the sole projection. Ordinary gRPC, HTTP/SSE, Team, and public EventLog read-back surfaces continue to omit the debugger-only structural fields. The debugger reports observations, not causal conclusions. Instrumentation performs no I/O or response buffering and preserves first-chunk delivery, retry, cancellation, and stream-idle semantics.

The contract was settled during plan review: the additive payload shape, two-field vocabulary, explicit availability semantics, and outer-resilience-attempt retention unit are authoritative here.

## Consequences

The debugger can distinguish observed provider-terminal structure from incomplete or unavailable
stream evidence after restart, including when the final assistant text itself is nonsensical.
The evidence remains bounded and content-free, but it cannot establish why a stream was anomalous
or assign responsibility to a provider, gateway, model, or Mecatl. Existing failure evidence and
legacy rows remain useful; only the new structural portion may be unavailable. Provider fixtures
must cover their supported semantic terminal behavior, while unsupported details remain unavailable.

The additive engine seam requires the normal API compatibility artifact and changelog workflow.
No new event kind, correlation hierarchy, coverage-marker event, retention oracle, or debugger
authority is introduced by this decision.

## See also

- [Issue #1676](https://github.com/stacklok/mecatl/issues/1676)
- [ADR 0254](./0254-session-debugger-admin-transport.md)
- [ADR 0255](./0255-sanitized-network-attempt-evidence.md)
- [ADR 0257](./0257-session-debugger-hardening.md)
- [Architecture overview](../architecture.md)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md)
