# ADR 0350 — Jev as an explicit delegated-model router backend

- Status: Accepted
- Date: 2026-09-21
- Scope: the operator-tier `models.router` configuration, root composition, and a new internal Typesafe/Jev adapter; no engine, provider-registry, wire, tool, or persistence boundary changes
- Supersedes: [ADR 0031](./0031-subagent-model-router.md) only for its requirement that the router classifier is a composition-built one-turn LLM engine when the operator explicitly selects the Jev backend; its callback, taxonomy mapping, routing precedence, fallback, breaker, and observability decisions remain in force
- Superseded by: none

## Context

The semantic model router currently spends one tool-less LLM turn to choose an operator-defined category for eligible delegated work. Composition maps that category to a same-provider model and passes it through the existing child-engine factories. [ADR 0042](./0042-taxonomy-gated-model-router.md) makes the operator taxonomy the enable decision and retains an explicit kill-switch.

Jev is a narrower decision service. The verified `typesafe-go` v0.1.0 API accepts untrusted state plus a trusted `Choice` criterion map and returns one exact choice, probabilities, confidence, model identity, request identity, and token usage. It is not a chat or generative provider, and treating it as `port.LLMProvider` would add a false provider identity and expose unrelated provider behavior.

Selecting Jev also creates a new external-data and credential boundary. Delegated task text leaves the Mecatl deployment, `TYPESAFE_API_KEY` becomes a harness credential, and a shared client plus concurrency limiter outlive one classification call. These are operator and system-boundary decisions, not adapter-local implementation details.

## Decision

Allow exactly two delegated-router backends: `llm` and `jev`. Keep `llm` as the default so existing configurations retain current behavior. Select Jev only through the operator-tier `models.router.backend: jev`; the presence of an endpoint, model, or `TYPESAFE_API_KEY` never selects or enables it. Taxonomy presence and `disabled: true` retain the [ADR 0042](./0042-taxonomy-gated-model-router.md) enable and kill-switch semantics.

Keep the engine contract unchanged. Both backends produce the existing `agent.Deps.SubagentModelRouter` callback result, and composition continues to own category-to-model resolution through `lookupModelAlias`. Routing eligibility, precedence, same-provider child construction, no-FS behavior, routing events, static miss reasons, and the three-consecutive-miss breaker do not vary by backend. The existing decide-once rules remain authoritative: [ADR 0034](./0034-team-parallel-model-routing.md) routes a member once at `AddMember` for that member's lifetime and each Parallel branch once, while [ADR 0066](./0066-route-unpinned-and-writable-delegations.md) routes eligible unpinned named specialists and writable explorers but treats any explicit definition model — including `model: inherit` — as pinned. A low-confidence Jev result, when confidence filtering is configured, is a miss and ordinary model inheritance. It does not force the LLM backend's advisory `default-category`.

Implement Jev as a root internal adapter, not an engine adapter or provider module. The adapter issues one `SystemOne` request whose `State` is the delegated task and whose single, fixed-identifier `Choice` question maps category names to operator descriptions. Fixed classifier instructions and model/question defaults keep its shape predictable. It accepts only an exact offered choice and fully valid response. It returns no partial category on validation or protocol error. No new generic classifier framework is introduced.

Extend the strict router configuration with this shape:

```yaml
models:
  router:
    backend: jev # llm (default) or jev
    jev:
      model: jev-1.13.0
      # base-url is normally omitted; override only with the intended service endpoint
      # base-url: https://api.typesafe.ai
      minimum-confidence: 0 # zero disables confidence filtering
```

`jev.model` defaults to the pinned `jev-1.13.0`; `jev.base-url` is optional and otherwise leaves endpoint selection to the pinned SDK; and `jev.minimum-confidence` defaults to zero and must always be finite in `[0,1]`, with zero disabling the threshold. `TYPESAFE_API_KEY` is the only credential source and is not configurable in YAML. Strict YAML/schema validation always applies, including when routing is disabled or has no taxonomy: reject unknown keys, wrong shapes, an unknown backend enum, and invalid confidence values. Only inactive-backend checks are skipped when `disabled: true` or the taxonomy is empty: do not require credentials or endpoint connectivity, construct a client, or enforce LLM/Jev selection conflicts. For an active router, reject Jev without the credential, reject a `jev` block under `backend: llm`, and reject explicitly authored `classifier-slot` or `default-category` under `backend: jev`; those fields control only the LLM classifier. Do not reject implicit slot defaults.

The importable engine's exported API remains unchanged. Host wiring does grow concrete internal Go surfaces: `internal/app.Config` gains `RouterBackend`, `RouterJevModel`, `RouterJevBaseURL`, `RouterJevMinimumConfidence`, and `TypesafeAPIKey`; `internal/adapter/permconfig.RouterSection` gains `Backend` and `Jev *JevRouterSection`, whose fields are `Model`, `BaseURL`, and `MinimumConfidence` (plus private presence tracking where selection-conflict validation requires it). Those identifiers are exported for composition and tests inside the repository, but Go's `internal` boundary means they are not a public library API.

Pin the client dependency `github.com/stacklok/typesafe-go` to SDK version v0.1.0, separately from pinning the service request's Jev model default to `jev-1.13.0`; neither pin follows a moving alias. Configure its client explicitly with `WithAPIKey`, `WithHTTPClient`, `WithDefaultModel`, the optional `WithBaseURL`, a 10-second `WithAttemptTimeout`, a zero-retry `WithRetryPolicy`, and a 1 MiB `WithResponseLimit`. Disable HTTP redirects. Allow HTTPS endpoints and loopback HTTP only. Use one client and one eight-slot semaphore per `app.Build`, with a 10-second queue wait and a 10-second request deadline. Caller cancellation while queued must release/no-leak capacity and must not send a request; cancellation in flight cancels the sole request and releases capacity. Queue timeout, request timeout, and cancellation are fail-soft misses, with no duplicate call.

Before SDK marshalling, compute one exact local text-input measure: sum the UTF-8 byte lengths of the task, fixed classifier instructions, selected model, fixed question identifier, and every category name and description. Reject a sum above 64 KiB or a taxonomy above 255 categories as static `jev-over-limit`, without I/O; never truncate the task or taxonomy. The request payload is limited to those adapter-controlled strings and maps and uses no user-defined `MarshalJSON`. The 64 KiB guard is not Jev tokenization and does not claim to detect every service token/context overflow. A server-side size/context rejection is an SDK/API miss and ordinary fallback. The 255-category bound follows current reviewed service guidance; neither bound is claimed to be a calibrated service default. The 1 MiB response cap bounds returned data, while request deadlines bound transport waiting; they do not create a hard end-to-end wall-clock guarantee over arbitrary CPU work in SDK JSON processing.

Preserve all independently validated reported usage exactly once through the existing callback fold. Map Jev input/output tokens to the existing `session.Usage` on hits and misses, including an otherwise valid response rejected for low confidence or failure of local category-to-model mapping. If a `ProtocolError` carries non-nil valid usage, return it with the miss, including reported zero. Nil or absent usage maps to the callback's existing zero value as "not reported," not as proof of zero spend. Never estimate missing usage or add an unknown-usage wire field. This reuses the existing unconditional usage fold in `engine/agent/dispatch.go` (`routeTaskBody`) and introduces no new accounting mechanism or token-accounting hardening.

Treat task state and the entire service response as sensitive, producer-influenced data. Diagnostics and events may contain only existing routing metadata and closed static miss reasons. They never contain the task, raw answer, probabilities, request ID, transport body, or credential. Add `TYPESAFE_API_KEY` to the exact environment deny and non-overridable sets in `internal/adapter/envscrub/envscrub.go`; suffix matching remains defense in depth. User documentation must state that selecting Jev sends delegated task text to Typesafe.

Inventory the shared client and semaphore as Build-owned process resources when implementation lands. They carry no durable session state and need no rehydration. Do not introduce a cache.

The operator directly approved these configuration, abstention, transport, and egress choices on 2026-09-21 and explicitly waived the merged-plan prerequisite for a stacked implementation targeting plan PR #1735; the plan PR remains subject to its human merge gate.

## Consequences

Operators can use a purpose-built decision model without presenting it as a chat provider or changing delegation tools and engine APIs. Existing installations remain on the current LLM classifier unless they explicitly select Jev. The same composition builder serves shared and per-session engines, preventing routing drift.

Jev adds one external dependency, credential, egress path, and bounded shared resource. Classification remains fail-soft, so an outage preserves delegation availability but loses routing quality. A low-confidence threshold can reduce forced choices, but no default above zero is claimed to be calibrated.

No result cache means repeated eligible tasks make repeated calls. Fixed concurrency, local input/response bounds, and transport deadlines bound the controlled request path without claiming a hard wall-clock bound over arbitrary SDK CPU work. Changing those constants later requires review against deployment behavior. Jev's reported usage can be retained without making unrelated token-accounting hardening a condition of adoption.

## See also

- [Jev delegated-model router acceptance plan](../acceptance/jev-model-router.md)
- [ADR 0031](./0031-subagent-model-router.md) for the existing callback, precedence, fallback, and breaker
- [ADR 0034](./0034-team-parallel-model-routing.md) for route-once team-member lifetime and per-branch Parallel routing
- [ADR 0066](./0066-route-unpinned-and-writable-delegations.md) for unpinned/writable routing and explicit `model: inherit` pinning
- [ADR 0042](./0042-taxonomy-gated-model-router.md) for taxonomy-based enablement and the kill-switch
- [Provider architecture](../architecture/providers.md#the-semantic-model-router-phase-5) for current router behavior
- [ADR 0027](./0027-cloud-native.md) for the resource-inventory convention
- [ADR 0002](./0002-documentation-lifecycle.md) for the decision-record lifecycle
