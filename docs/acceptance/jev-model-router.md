# Jev delegated-model router — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — selecting an external decision service changes the operator configuration, credential, data-egress, and adapter boundaries of delegated-model routing.
**Decision record:** [ADR 0350](../adr/0350-jev-delegated-model-router.md)
**Phase:** delegated-model router backend
**Status:** landed, 2026-09-21. Implementation candidate: this transition becomes authoritative only after human merge. The operator explicitly waived the merged-plan prerequisite for the stacked implementation; the plan PR is not yet merged.
**Delivery:** Split. The implementation PR targets the plan branch under that explicit waiver; both PRs retain human merge gates.
**Expected tasks:** deferred to orchestration
**Plan PR:** [#1735](https://github.com/stacklok/mecatl/pull/1735).
**Approved baseline:** `14923277ec157b299913a66e2be6304671b0be2c`, approved directly by the operator for stacked implementation.

This plan adds Jev as an explicitly selected classifier backend for the existing semantic model router. It preserves the current taxonomy, category-to-model mapping, same-provider child construction, precedence, fallback, observability, and per-run breaker described in [ADR 0031](../adr/0031-subagent-model-router.md) and [ADR 0042](../adr/0042-taxonomy-gated-model-router.md).

The proposal uses `typesafe-go` v0.1.0 with Jev model `jev-1.13.0`. Jev is a bounded decision adapter, not an LLM provider: it does not enter the provider registry, implement `port.LLMProvider`, route main-session turns, or change the engine callback.

## Human decisions

- [x] Keep this contract router-only — Decision: guardrails, escape checking, approval reviewers, cross-provider or main-session routing, per-turn routing, caches, and token-accounting hardening are outside this plan. Existing reported router usage continues through the current accounting seam without making independent accounting work a prerequisite.
- [x] Operator surface — Decision: retain the reviewed proposal: `backend: llm|jev` defaults to `llm`; Jev has `model`, `base-url`, and `minimum-confidence`; `TYPESAFE_API_KEY` is its only credential source; explicit LLM-only keys conflict with an active `backend: jev`; and disabled or taxonomy-free routing skips inactive-backend credential, connectivity, and selection-conflict checks while strict YAML/schema validation still applies.
- [x] Abstention behavior — Decision: an absent or zero `minimum-confidence` disables confidence filtering; a valid answer below an explicitly configured threshold is a router miss that inherits the normal child model and counts toward the existing breaker, rather than selecting `default-category`.
- [x] External-call policy — Decision: one shared client and eight-slot semaphore per `app.Build`, a 10-second queue wait, a 10-second request deadline, a 1 MiB response limit, a 64 KiB locally measured UTF-8 text-input cap, at most 255 categories, no SDK retries, no redirects, HTTPS except loopback test/development endpoints, and explicit documentation that delegated task text leaves the deployment when Jev is selected.

## Interface contract

- **gRPC / protobuf:** None — backend selection is operator-local composition and reuses existing delegation events. No request, response, service, or field changes are proposed.
- **Exported Go APIs / interfaces:** The importable `engine` API remains unchanged — keep `agent.Deps.SubagentModelRouter` exactly `func(context.Context, string) (category, model string, usage session.Usage, missReason string, ok bool)` and add no engine port, provider entry, or exported SDK wrapper. Host composition must add the minimum concrete fields needed to carry this operator choice: `internal/app.Config` gains `RouterBackend`, `RouterJevModel`, `RouterJevBaseURL`, `RouterJevMinimumConfidence`, and `TypesafeAPIKey`; `internal/adapter/permconfig.RouterSection` gains `Backend` and `Jev *JevRouterSection`, whose fields are `Model`, `BaseURL`, and `MinimumConfidence`. These Go identifiers are exported within `internal` packages for composition and tests but are not a public library API because Go's `internal` import boundary excludes external consumers. Do not introduce a generic classifier framework. Offline dependency injection follows the ordinary concrete-client/`httptest` reference pattern; no new testing framework is proposed.
- **Tool schemas:** None — Subagent, Team, and Parallel schemas and routing eligibility remain unchanged.
- **CLI / config:** Extend the strict, operator-tier-only `models.router` mapping with the following proposed keys. `backend` accepts `llm` or `jev` and defaults to `llm`. `jev.model` defaults to `jev-1.13.0`; `jev.base-url` is optional and otherwise uses the pinned SDK's official endpoint; `jev.minimum-confidence` defaults to `0` (filter disabled) and must always be finite and in `[0,1]`. `TYPESAFE_API_KEY` supplies the credential and is not representable in YAML. A non-empty taxonomy still enables routing, and `disabled: true` still wins. Strict decoding and schema validation always run, even when routing is disabled or has no taxonomy: unknown keys, wrong shapes, an unknown backend enum, and an out-of-range/non-finite confidence are invalid configuration. Only active-backend checks are skipped when disabled or taxonomy-free: no credential or endpoint connectivity requirement, no client construction, and no LLM/Jev selection-conflict rejection. When active, `backend: jev` conflicts with an explicitly authored `classifier-slot` or `default-category`; inherited slot defaults do not conflict. An active `backend: llm` conflicts with a `jev` block. Selecting active Jev without a non-empty credential is an error. An endpoint or credential alone never enables Jev. No new CLI flag is proposed.

  ```yaml
  models:
    router:
      backend: jev
      jev:
        model: jev-1.13.0
        # base-url is normally omitted; override only with the intended service endpoint
        # base-url: https://api.typesafe.ai
        minimum-confidence: 0
      categories:
        - name: fast
          description: Small, well-scoped changes
          model: cheap
        - name: deep
          description: Cross-cutting design and difficult debugging
          model: capable
  ```
- **Events / persistence:** No event or snapshot shape changes. Jev hits populate the existing routed category/model fields; misses use existing bounded routing-reason projection, with new static reasons proposed as `jev-error`, `jev-invalid-response`, `jev-low-confidence`, `jev-over-limit`, and `jev-queue-timeout`. The adapter returns independently validated Jev input/output usage through the existing callback exactly once on hits and misses, including when an otherwise valid response is rejected for confidence or failure of the local category-to-model mapping. A `ProtocolError` likewise retains non-nil validated usage. No reported usage is represented by the callback's existing zero value; that means unknown/not reported, not proven zero spend, and this plan adds no unknown-usage wire field. The shared HTTP client and semaphore are process resources and must be added to the cloud-native resource inventory during implementation; neither carries durable session state.
- **Security / authority:** Only operator-tier `settings.yaml` may select Jev. The delegated task prompt is untrusted state sent to Typesafe, while category names and descriptions are trusted operator criteria. Jev is not treated as generative, conversational, or prompt-injection-proof. The adapter must not log or emit the task, raw response, answers, API key, or transport bodies. Add `TYPESAFE_API_KEY` to `envscrub.DenyExact` and `envscrub.NonOverridableExact`; suffix scrubbing remains defense in depth. The HTTP client disables redirects, permits HTTPS endpoints and loopback HTTP only, and applies bounded response and request limits.
- **Compatibility / migration:** Existing configuration remains on the `llm` backend by default and behaves byte-for-byte as today. Jev is opt-in through `backend: jev`; inactive routing creates no client or network capability. Pin the client dependency `github.com/stacklok/typesafe-go` to SDK version v0.1.0, independently from pinning the service request's Jev model default to `jev-1.13.0`; neither pin follows a moving alias. Implementation must update `user-docs/features/choose-models.md`, the generated configuration reference source, and applicable deployment credential pages for `mecated`, `mecatequi`, `mecak8s`, and embedded `mecatui`; generated Markdown is not hand-edited.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — Operator selects one router backend

The strict operator configuration owns the backend decision, while taxonomy presence and the existing kill-switch retain the enable model in [ADR 0042](../adr/0042-taxonomy-gated-model-router.md).

**Acceptance:**
- AC1.1: configuration with no `backend` continues to build the existing LLM classifier, and `backend: jev` builds the Jev adapter only when routing is enabled.
  - verify: `TestADR_0350_Scenario1_BackendSelection`
- AC1.2: strict parsing rejects unknown keys, wrong shapes, an unknown backend, and invalid confidence values regardless of `disabled` or taxonomy presence. Disabled or taxonomy-free routing prevents client construction and skips only inactive-backend credential, connectivity, and selection-conflict checks. An active Jev router rejects a missing credential and explicitly authored `classifier-slot` or `default-category`; an active LLM router rejects a `jev` block.
  - verify: `TestADR_0350_Scenario1_ValidationAndDisabledPrecedence`
- AC1.3: project-tier router configuration cannot select Jev, and endpoint or credential presence alone does not enable or select it.
  - verify: `TestADR_0350_Scenario1_OperatorAuthority`

### Scenario 2 — Jev makes one validated category decision

Composition sends the task as untrusted `SystemOneRequest.State` and asks one `Choice` question whose trusted criteria map category names to descriptions. Fixed classifier instructions, a fixed question identifier, and a defaulted model keep the request shape predictable. The chosen category still resolves through the shared alias/category mapping in `internal/app/build.go` (`buildModelRouterTask`), preserving the category boundary from [ADR 0031](../adr/0031-subagent-model-router.md), the team/member lifetime rules from [ADR 0034](../adr/0034-team-parallel-model-routing.md), and the unpinned/writable precedence from [ADR 0066](../adr/0066-route-unpinned-and-writable-delegations.md).

**Acceptance:**
- AC2.1: a valid exact Jev choice routes each eligible Subagent invocation, an unpinned named specialist, and a writable explorer through the existing callback. Team members route once at `AddMember` and retain that model for the member lifetime; each Parallel branch routes once for that branch. Explicit per-call or definition pins, including explicit `model: inherit`, as well as fork and resume continue to bypass routing.
  - verify: `TestADR_0350_Scenario2_ExistingRoutingSemantics`
- AC2.2: unknown choices, malformed responses, non-finite probability data, request-size violations, and SDK/protocol errors return no partial category and fall back to the ordinary inherited model.
  - verify: `TestADR_0350_Scenario2_InvalidResponseFallsBack`
- AC2.3: when `minimum-confidence` is nonzero, confidence below the threshold is `jev-low-confidence`; it is a miss for the existing three-miss per-run breaker and does not force `default-category`.
  - verify: `TestADR_0350_Scenario2_LowConfidenceAbstains`

### Scenario 3 — Usage survives success and failure without invention

The existing dispatch seam in `engine/agent/dispatch.go` (`routeTaskBody`) already records callback usage on router hits and misses, as required by [ADR 0031](../adr/0031-subagent-model-router.md). The adapter preserves Jev's independently validated usage rather than creating another accounting path.

**Acceptance:**
- AC3.1: successful responses map Jev input and output tokens to `session.Usage` and return them with the route hit. An otherwise valid response rejected for low confidence or failure of the local category-to-model mapping returns the same reported usage with the miss; the existing parent fold records it exactly once.
  - verify: `TestADR_0350_Scenario3_HitUsage`
- AC3.2: a `ProtocolError` with non-nil validated usage returns that usage with the miss, including reported zero. Nil or absent usage maps to the callback's existing zero value as "not reported," not as proof that the service spent zero tokens; no spend is estimated and no unknown-usage field is added.
  - verify: `TestADR_0350_Scenario3_ErrorUsage`
- AC3.3: the shared and per-session engine paths use the same backend builder and category alias lookup, so provider-fixed child construction and no-FS behavior do not diverge.
  - verify: `TestADR_0350_Scenario3_CompositionParity`

### Scenario 4 — External routing is bounded and secret-safe

The external call follows the command-credential boundary in [AGENTS.md](../../AGENTS.md) and the router's fail-soft behavior in [ADR 0031](../adr/0031-subagent-model-router.md). The proposed local request limit is deliberately byte-measurable, not a claim about Jev tokenization or service context acceptance.

**Acceptance:**
- AC4.1: the adapter permits at most eight concurrent requests per Build, gives up after a 10-second queue wait, applies a 10-second request deadline and 1 MiB response limit, configures zero SDK retries, disables redirects, and rejects non-loopback cleartext endpoints before sending data. Caller cancellation while queued releases/no-leaks semaphore capacity and sends no request; cancellation in flight cancels the one request and releases capacity. Every timeout/cancellation remains fail-soft and no path duplicates a classification call.
  - verify: `TestADR_0350_Scenario4_BoundedTransport`
- AC4.2: before SDK marshalling, the adapter sums the UTF-8 byte lengths of exactly the task, fixed classifier instructions, selected model, fixed question identifier, and every category name and description. A sum over 64 KiB or more than 255 categories returns static `jev-over-limit` without I/O; the adapter never truncates the task or taxonomy. The SDK request contains only these controlled strings/maps and uses no user-defined `MarshalJSON`. The 64 KiB bound is a proposed local text-input guard, not an authoritative tokenizer or guarantee that every service-token/context overflow is detected; server-side size/context rejection remains an SDK/API miss and ordinary fallback. The 255-category cap follows current reviewed service guidance. Fixed question/model defaults keep payload shape predictable.
  - verify: `TestADR_0350_Scenario4_RequestLimits`
- AC4.3: the 1 MiB cap bounds response data and request deadlines bound transport waiting, but the plan claims no hard end-to-end wall-clock limit over arbitrary CPU work performed by SDK JSON processing.
  - verify: `TestADR_0350_Scenario4_BoundedTransport`
- AC4.4: `TYPESAFE_API_KEY` cannot enter any agent-facing command environment, and diagnostics/events contain only static reasons and routing metadata, never credentials, task text, raw answers, or response bodies.
  - verify: `TestADR_0350_Scenario4_SecretAndContentRedaction`
- AC4.5: offline tests use ordinary injected clients and fake or `httptest` endpoints, perform no live Jev call or private payload upload, and require no new testing framework.
  - verify: inspection — test fixtures and CI configuration must contain no live Typesafe endpoint invocation

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Guardrails, escape checking, and approval reviewers | Separate contracts | They do not reuse this router backend in this plan. |
| Main-session, cross-provider, or per-turn routing | Separate architectural work | Jev only chooses a category for currently eligible delegated work. |
| Router result caches | Later optimization | Every eligible decision remains live and the current breaker remains run-scoped. |
| Token-accounting hardening | Independent work | Preserve reported Jev usage through the existing callback; do not make unrelated accounting changes an adoption condition. |
| New provider, engine port, proto, or tool surface | Not planned | Jev remains an internal decision adapter behind the existing callback. |

## Definition of done

1. `task lint`, `task test`, `task docs`, and `task api:check` pass; all tests remain offline.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. The implementation updates the owning public guides, generated configuration source/output, architecture and implementation notes, and cloud-native resource inventory.
5. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Jev receives delegated task text. Explicit operator selection is consent to this egress, but the user-facing wording and deployment examples must make the boundary unmistakable.
- Category descriptions are operator-controlled criteria, but task state can contain prompt injection. Exact choice validation bounds the result; it does not make Jev an authorization or safety classifier.
- SDK and service-model versions, service limits, and model names can change independently. The implementation pins SDK v0.1.0 separately from service model `jev-1.13.0`; changing either pin requires an ordinary reviewed dependency/config-default change rather than silently following an alias. The proposed 64 KiB input-byte and 255-category guards are local safety bounds, not calibrated token limits or guarantees of service acceptance.
