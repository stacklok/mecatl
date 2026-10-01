# Slack author attribution — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — the Slack reference bot will render verified sender attribution into its existing user-message text without changing the engine message model, public APIs, session schema, or authorization policy.
**Decision record:** None — this is a deliberately narrow Slack prompt-rendering contract, not the durable multi-user identity architecture.
**Phase:** Slack integration usefulness
**Status:** proposed, 2026-10-01. Derived from [issue #2029](https://github.com/stacklok/mecatl/issues/2029) and the directing human's decision to defer a full identity model.
**Delivery:** Split. The prompt representation is model-visible behavior and its trust boundary merits independent interface review before implementation.
**Expected tasks:** 2
**Issue:** [stacklok/mecatl#2029](https://github.com/stacklok/mecatl/issues/2029).
**Plan PR:** absent until opened
**Approved baseline:** absent until approved

The Slack bot will render every accepted inbound Slack message into the existing text submitted to its Mecatl session. The rendered text will preserve the source message as data and prepend a stable Slack user ID; where Slack returns a display name, it will add that name only as presentation metadata. This lets the agent distinguish contributors in a persisted conversation without treating Slack identity as a new core-session author model.

The renderer is intentionally Slack-local and is the canonical representation that the later thread-history reconstruction work in [#2030](https://github.com/stacklok/mecatl/issues/2030) must use. A broader, provider-neutral and multi-user identity design requires a separately reviewed ADR before implementation.

## Human decisions

None — the directing human selected the deliberately temporary prompt-level attribution boundary: persist the rendered attribution in the existing user-message text, do not add a core message-author field or ADR, and require a future ADR before a full identity or multi-user implementation.

## Interface contract

- **gRPC / protobuf:** None — the Slack bot continues to invoke the existing SDK session-run operation with one rendered text prompt.
- **Exported Go APIs / interfaces:** None — `engine/session.Message`, `engine/agent.RunRequest`, and the engine API surface remain unchanged.
- **Tool schemas:** None — this change adds no tool or tool-argument surface.
- **CLI / config:** None — no Slack bot or Mecatl configuration key changes.
- **Events / persistence:** Existing `EvUserPrompt` and session snapshots retain the rendered attribution as their existing text field; no new event, snapshot field, or migration is introduced. A session replay therefore preserves the same model-visible author distinction.
- **Security / authority:** Slack user IDs originate only from accepted Slack events after the existing access resolver and rate limiter permit the event. The allowed `AccessDecision` gains only an optional `displayName` presentation value, sourced by `EmailAllowlistResolver` from a non-empty Slack `profile.display_name`; custom resolvers may omit it. The ID is authoritative only as Slack-source attribution, not as Mecatl caller identity or authorization. Display names and message bodies are untrusted presentation data and must be clearly delimited so neither can be interpreted as instructions or replace the stable ID.
- **Compatibility / migration:** Additive behavior limited to new Slack-bot user prompts. Existing sessions retain their historical raw prompts; no backfill, conversion, or engine/API compatibility change occurs.

## In scope — 2 scenarios, in implementation order

### Scenario 1 — Every accepted Slack turn has unambiguous author attribution

The bot already routes DMs, channel mentions, and replies in activated channel threads through the same prompt bridge, but passes raw `text` to it ([`agentSessions.ts`](../../sdk/typescript/examples/slack-bot/src/agentSessions.ts)). The bridge persists the text by running it through the existing session SDK ([`bridge.ts`](../../sdk/typescript/examples/slack-bot/src/bridge.ts)). This follows the agent loop's existing user-text recording boundary ([agent loop](../architecture/agent-loop.md)); the renderer belongs at Slack ingress, after acceptance and before the bridge call.

**Acceptance:**
- AC1.1: An accepted DM, channel mention, or reply in an activated channel thread submits a rendered prompt containing the sender's stable Slack user ID and the original message content as separately labelled, explicitly delimited data.
  - verify: `agentSessions.test.ts` coverage invokes each ingress path and asserts the bridge receives the canonical rendered prompt.
- AC1.2: Two accepted messages in one Slack thread from different user IDs yield distinct persisted prompt text whose sender IDs remain distinguishable, including when their display names or message text match.
  - verify: `agentSessions.test.ts` coverage drives distinct senders through an active thread and asserts the two bridge prompt arguments.
- AC1.3: A non-empty Slack `profile.display_name`, when returned by the existing `EmailAllowlistResolver` lookup, is rendered only as optional presentation metadata; an absent display name or a custom resolver that does not supply one neither rejects an accepted message nor omits its stable user ID.
  - verify: `access.test.ts` and `agentSessions.test.ts` coverage for present and absent display names.
- AC1.4: Display names and message content that resemble instructions, delimiters, or identity fields remain escaped or framed as untrusted data and cannot replace, suppress, or create an authoritative Slack user ID.
  - verify: `agentSessions.test.ts` adversarial rendering cases.

### Scenario 2 — Attribution has one Slack-local representation for future replay

Slack sessions persist their user-message text through Mecatl's existing conversation and event path; no new author field is added to the core `Message` value object ([`conversation.go`](../../engine/session/conversation.go)). The renderer must preserve the repository's canonical untrusted-content fences ([`AGENTS.md`](../../AGENTS.md)), and be a small Slack-local component rather than separately constructed strings in the three ingress handlers.

**Acceptance:**
- AC2.1: All supported inbound paths use one canonical renderer, so equivalent Slack sender metadata and text produce byte-identical prompt representations regardless of DM, mention, or active-thread-reply origin.
  - verify: `agentSessions.test.ts` cross-ingress representation assertions and code inspection of the single renderer call site.
- AC2.2: The renderer has no authority side effect: access decisions, per-user rate limits, Slack approval routing, and recipient selection continue to use the original Slack user ID rather than rendered prompt text or display name.
  - verify: existing authorization, rate-limit, and approval tests in `agentSessions.test.ts` remain green; focused regressions assert the resolver and rate limiter receive the raw user ID.
- AC2.3: The rendering function is available to the Slack thread-history reconstruction implementation described by [#2030](https://github.com/stacklok/mecatl/issues/2030), without introducing a core identity field.
  - verify: inspection — the renderer is exported from a Slack-bot-local module with an input shape independent of a live Bolt event.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Provider-neutral, first-class `Message` author identity | Future multi-user identity work | Requires a separate ADR and Architectural acceptance plan before implementation. |
| Persisted Slack thread-to-Mecatl-session mapping | Future Slack reliability work | Not required by #2029; [#2030](https://github.com/stacklok/mecatl/issues/2030) reconstructs history when no usable mapped session exists. |
| Reconstructing prior Slack thread history | [#2030](https://github.com/stacklok/mecatl/issues/2030) | That work reuses this renderer for reconstructed user turns. |
| Changing Slack authorization, rate limiting, approval policy, or caller ownership | None | Existing policies remain authoritative and unchanged. |

## Definition of done

1. Focused Slack-bot tests, `task slack-bot:lint`, `task slack-bot:typecheck`, and `task slack-bot:test` pass.
2. Applicable `task lint`, `task test:race`, `task docs`, and `task api:check` gates pass on the final candidate.
3. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green for runtime changes.
5. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The exact delimiters and escaping must remain legible to the model while treating all Slack-derived values as data; implementation chooses the smallest representation that satisfies Scenario 1's assertions.
- A future durable identity model must define verified identity stamping, generic source namespaces, API/snapshot exposure, and migration in an ADR rather than promoting this Slack rendering convention into core semantics.
