# ADR 0110 — Correlate provider requests with the active session

- Status: Accepted
- Date: 2026-08-14
- Scope: Agent run context and real HTTP LLM-provider adapters
- Supersedes: None
- Superseded by: None

## Context

Operators need to correlate an outbound inference request with the durable mecatl
session that caused it, especially when requests pass through a gateway. The model
request DTO must remain provider-neutral, provider instances are shared by concurrent
sessions, and nested engines have identities distinct from their parents.

No standard HTTP field means "durable coding-harness conversation identity".
`traceparent` identifies a trace, W3C baggage carries arbitrary distributed context,
and OpenTelemetry's `gen_ai.conversation.id` is telemetry data rather than an HTTP
wire contract. Provider body fields, safety/user identifiers, prompt-cache keys, and
idempotency keys each have provider-specific semantics that session correlation must
not acquire.

## Decision

Send the proprietary `X-Mecatl-Session-ID` request header from the real OpenAI
Responses, OpenAI Chat Completions, and Anthropic Messages adapters.

Bind the exact active `session.SessionID` once in the shared run-entry context at
`engine/agent/loop.go` (`startRun`) using the context helpers in
`engine/port/sessioncontext.go`. That foundation shipped in `engine/v0.11.0`.
Keep `port.LLMRequest` unchanged. Every engine binds
its own actual session ID, overwriting any inherited parent binding; provider calls
inside that run, including compaction, inherit it. Reopened, recovered, and
awaiting-resume runs therefore use the loaded persisted ID.

Project the value with per-request SDK options in
`provider/openai/openai.go`, `provider/openaichat/openaichat.go`, and
`provider/anthropic/anthropic.go`. Never configure it on a shared provider client.
Retries and the OpenAI encrypted-reasoning fallback reuse the same per-request
option. If the context value is absent or contains a byte that Go rejects in an HTTP
header field value, omit the header and continue the inference request. Do not
encode, normalize, truncate, log, or reject the request because of this optional
metadata.

The header is correlation-only. It is not authentication, authorization, tracing,
idempotency, provider conversation state, safety identity, or cache identity.

## Consequences

Gateways and provider-side request logs can correlate inference traffic to the exact
mecatl session without parsing prompts or changing the neutral request DTO. Child,
member, and auxiliary engines report their own IDs; compaction performed within a
parent run reports the parent's ID.

The field is proprietary, so external infrastructure must opt in to recording it.
Session IDs become outbound metadata visible to the selected provider or gateway.
Consumers implementing their own `port.LLMProvider` can read the context helper but
are not required to use HTTP or emit this header. Invalid values silently lose this
optional correlation signal rather than risking inference availability.

## See also

- [Provider architecture](../architecture/providers.md)
- [LLMProvider extension point](https://github.com/stacklok/mecatl/blob/main/user-docs/extension-points/llm-provider.md)
- [ADR 0016](./0016-multi-provider.md)
- [ADR 0093](./0093-provider-modules.md)
