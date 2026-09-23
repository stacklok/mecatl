# ADR 0255 — Sanitized durable network-attempt evidence

- Status: Accepted
- Date: 2026-08-31
- Scope: provider-attempt evidence for stored-session debugging
- Supersedes: ADR 0254's deferral of sanitized network-attempt timing only
- Superseded by: —

## Context

ADR 0254 established a target-bound session debugger but deferred sanitized network-attempt timing. Existing diagnostics describe retry decisions only while a process is live and are not a safe evidence source for a later debugger run. Retaining raw provider failures would risk persisting credentials, request identifiers, URLs, prompts, tool arguments, or response content. Sending retry evidence over ordinary client event streams would also broaden a debugger-only surface.

## Decision

Record each failed or suppressed provider attempt as a provider-neutral, sanitized `network.attempt` event. The resilience wrapper constructs one `session.NetworkAttemptPayload` from the same decision and metadata classification used by diagnostics and reports it through a run-local `port.AttemptObserver`. The loop canonicalizes target, run, and turn correlation and emits the event; the existing server relay persists it through `port.EventLog`. The resilience adapter never writes storage directly.

The payload may retain attempt ordinal, maximum attempts, elapsed and backoff milliseconds, retry disposition, stream progress, retry or terminal decision, suppression reason, timeout/idle/breaker or provider-neutral transport class, validated HTTP or in-band status, and a closed correlation kind with a domain-separated SHA-256 digest. It must never retain raw errors, provider codes, correlation identifiers, URLs, queries, headers, bodies, prompts, tool arguments, cookies, credentials, tokens, or environment values. Successful attempts and DNS/TCP/TLS phase timings are not measured.

`network.attempt` is debugger/EventLog-only. It has no public protobuf projection and every ordinary client relay suppresses it, including Converse, live session streams, durable event-log read-back, and direct Team gRPC and HTTP/SSE streams. Filtering is transport-local and does not gate durable observation, so client visibility cannot change persistence. Ordinary Team events and terminal outcomes continue to flow.

The target-bound `InspectSession` `network` view is the only model-visible projection. It scans at most 10,000 events, returns at most 50 rows, fences the evidence as untrusted, and reports availability, scan completeness, pagination, truncation, and unavailable measurements explicitly.

## Consequences

A debugger can distinguish retry, timeout, stream-idle, breaker, rate-limit, and terminal suppression behavior after restart without exposing raw provider material. Evidence remains bounded by EventLog retention and the view limits, and it cannot provide packet capture, successful-attempt latency, or protocol-phase timing.

Adding a new direct event transport requires applying the debugger-only visibility predicate without bypassing durable observation.

## See also

- [ADR 0254](./0254-session-debugger-admin-transport.md)
- [Cloud-native arc](./0027-cloud-native.md)
- [Architecture overview](../architecture.md)
- [Implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md)
- [Production readiness tracker](../design/PRODUCTION-READINESS.md)
