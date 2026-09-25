# ADR 0360 — Separate root-conversation correlation from active-session identity

- Status: Accepted
- Date: 2026-09-24
- Scope: agent run context, provider calls made inside main/delegated run trees, direct teams, official LLM-provider composition, and the Jev delegated-model router.
- Supersedes: ADR 0216 only where one outbound header was the complete correlation surface; retains its active-session header semantics.

## Context

`X-Mecatl-Session-ID` identifies the exact session whose engine makes an outbound model call. That identity is also an optional client-to-server affinity contract: official clients send it for session-bound requests, transports validate it against the authoritative request session, and external infrastructure may route on it. Nested engines deliberately replace it with their own Subagent, Parallel-branch, team-member, judge, or model-router session identity.

Operators also need to group provider and delegated-router requests within one main run tree without erasing those active child identities. Reusing `X-Mecatl-Session-ID` for that purpose would make the field ambiguous, break exact ingress-affinity semantics, and remove the child identity needed for per-session transcripts, retries, diagnostics, and accounting. Trace context is optional and describes an execution trace rather than a durable conversation correlation key.

## Decision

Add a second proprietary outbound field, `X-Mecatl-Root-Session-ID`, carrying the root conversation session ID for the current run tree. Preserve `X-Mecatl-Session-ID` unchanged as the exact active session ID. Main-session provider calls therefore send both fields with the same value; nested Subagent, Parallel, team-member, model-router, judge, reviewer, and guardrail calls send their own active ID in `X-Mecatl-Session-ID` and the causal root in `X-Mecatl-Root-Session-ID`.

The engine port owns context helpers for this correlation value. At run entry, an absent root is initialized from the session being run; an inherited non-empty trusted-composition root is preserved across nested `Engine.Run` calls. Official server run boundaries always overwrite caller context with the authoritative loaded/source session before entering a run tree, so transport metadata or a pre-populated request context cannot select the root. The exported setter is for trusted library composition, not header decoding. The root identifies the causal tree of the current invocation, not persisted lineage or authority. A resumed child invoked by another current root uses that current root while retaining its durable original relationship metadata. A direct team derived from a source session retains that source in its process-local declaration and seeds it when `RunTeam` begins; a direct team created on default placement roots each member in itself because it has no source conversation.

Official composition wraps the final HTTP clients used by OpenAI Responses, OpenAI Chat Completions, and Anthropic Messages with a request-scoped transport decorator that projects the root value on every initial attempt, retry, and provider-specific fallback. The independently versioned provider modules remain unchanged and continue projecting the active header themselves, avoiding an unreleased engine dependency under ADR 0093. The same decorator attaches the root field to each Jev classifier HTTP attempt without changing Jev's zero-SDK-retry policy. It clones requests rather than mutating a shared client or request, preserves existing redirect refusal and credential transports, and cannot cross-stamp concurrent roots.

The exact root-header predicate is 1–256 bytes of printable ASCII (`0x20`–`0x7e`) with no leading or trailing space. If the value is absent or outside that set, the decorator omits only `X-Mecatl-Root-Session-ID` and continues. It does not encode, normalize, truncate, log, or reject inference because of optional correlation metadata. `X-Mecatl-Root-Session-ID` is outbound-only: official clients do not send it, server ingress ignores it rather than accepting it as affinity, and gateways must not use it to select the session-owning replica.

Both fields are correlation metadata only. The root field grants no authentication, authorization, ownership, permission, lease, fencing, tracing, idempotency, cache, safety-identity, provider-state, persistence, or cancellation semantics. Ingress metadata is never promoted into root context; only authoritative server/library composition seeds it. Asynchronous title generation, evidence reflection, learning, and other direct provider calls outside an engine run tree remain outside this decision until they have an explicit authoritative source and lifecycle contract.

## Consequences

Provider and Jev gateway logs can group model work inside one run tree while retaining the executing child or auxiliary-engine identity. Existing consumers of `X-Mecatl-Session-ID` remain byte-compatible, and infrastructure affinity behavior is unchanged. Custom `port.LLMProvider` implementations may read the root context helper but are not required to emit HTTP metadata.

The exported engine port gains additive context helpers and therefore requires API snapshots plus an Added/minor changelog entry. Official composition and the Jev adapter need concurrent-isolation and retry/fallback coverage for both headers; standalone provider modules remain unchanged and continue passing under `GOWORK=off`. Living provider and deployment documentation must distinguish the active affinity field from the outbound-only root field.

Session IDs, relationships, transcripts, stores, events, leases, ledgers, and tool schemas remain unchanged. The additional field exposes the main conversation ID to the same selected provider, gateway, or classifier that already receives the request; deployments should treat it as potentially user-correlatable metadata.

## See also

- [ADR 0216 — Correlate provider requests with the active session](./0216-provider-session-correlation-header.md)
- [ADR 0294 — End-to-end session correlation and affinity](./0294-session-correlation-and-affinity.md)
- [ADR 0093 — Provider modules](./0093-provider-modules.md)
- [Provider architecture](../architecture/providers.md)
- [Root-conversation provider correlation acceptance plan](../acceptance/root-session-provider-correlation.md)
