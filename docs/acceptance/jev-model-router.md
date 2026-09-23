# Jev delegated-model router - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural - selecting an external decision service and exposing durable routing-decision evidence change the operator configuration, exported engine API, wire, persistence, credential, data-egress, and adapter boundaries of delegated-model routing.
**Decision record:** [ADR 0350](../adr/0350-jev-delegated-model-router.md)
**Phase:** delegated-model router backend
**Status:** proposed, 2026-09-23 - Plan / Interface PR #1735 and stacked implementation PR #1738 remain open. All decisions below, including the configurable Jev input bound, shared Build-wide capacity, exact Team parent-call correlation, end-to-end durable evidence proofs, and transport/UI presence semantics, are directly approved in conversation. Direct approval is not a claim that the plan PR merged. The earlier explicit waiver of the merged-plan prerequisite continues to apply to the stacked implementation.
**Delivery:** Split. The external decision boundary and exact operator, engine, wire, and evidence contracts require review before implementation.
**Expected tasks:** deferred to orchestration

This plan adds Jev as an explicitly selected classifier backend for the existing semantic model router and makes each routing decision explainable through the existing delegation lifecycle. It preserves the current taxonomy, category-to-model mapping, same-provider child construction, precedence, fallback, and per-run breaker described in [ADR 0031](../adr/0031-subagent-model-router.md) and [ADR 0042](../adr/0042-taxonomy-gated-model-router.md).

The proposal uses `typesafe-go` v0.1.0 with Jev model `jev-1.13.0`. Jev is a bounded decision adapter, not an LLM provider: it does not enter the provider registry, implement `port.LLMProvider`, route main-session turns, or change provider selection.

## Human decisions

- [x] Keep this contract router-only - Decision: guardrails, escape checking, approval reviewers, cross-provider or main-session routing, per-turn routing, caches, new evaluation commands, metrics, storage backends, latency/cost/probability-vector evidence, and token-accounting hardening are outside this plan. Existing reported router usage continues through the current accounting seam.
- [x] Approve the operator surface - Decision: `backend: llm|jev` defaults to `llm`; Jev has `model`, `base-url`, `minimum-confidence`, and `maximum-input-bytes`; `TYPESAFE_API_KEY` is its only credential source; an explicitly authored LLM-only `classifier-slot` conflicts with an active `backend: jev`; and disabled or taxonomy-free routing skips inactive-backend credential, connectivity, and selection-conflict checks while strict YAML/schema validation still applies.
- [x] Approve the shared `default-category` advisory hint - Decision: the existing common routing policy is accepted for both LLM and Jev. A non-empty trimmed category appends `If no category clearly fits, choose %q.` to classifier instructions; absent, empty, or whitespace-only values add no hint. It never widens the offered category set or becomes an automatic runtime fallback.
- [x] Approve abstention behavior - Decision: an absent or zero `minimum-confidence` disables confidence filtering; a valid answer below an explicitly configured threshold is a router miss that inherits the normal child model and counts toward the existing breaker, rather than selecting `default-category`.
- [x] Approve the external-call policy - Decision: one shared client and one real eight-slot semaphore per `app.Build`, shared by the shared engine and every per-session engine; a 10-second queue wait; a 10-second request deadline; a 1 MiB response limit; operator-only `models.router.jev.maximum-input-bytes`, defaulting to 16384 and accepting integers from 1 through 65536 under an immutable 64 KiB hard ceiling; at most 255 categories; no SDK retries; no redirects; HTTPS except loopback test/development endpoints; and explicit documentation that delegated task text leaves the deployment when Jev is selected.
- [x] Approve the backend-neutral router outcome contract - Decision: the engine owns the shared closed miss taxonomy; Jev returns only an adapter-local typed status and composition exhaustively maps it before the engine callback. The approved mapping and evidence requirements are recorded below; unshipped `jev-*` values are replaced rather than migrated on the wire.
- [x] Approve capability-oriented classification and shared decision evidence - Decision: both backends classify by required expertise, specialty, and reasoning difficulty while treating task text as data rather than routing instructions. Every configured-router decision gets one bounded `RoutingDecision` snapshot on the existing Subagent, Parallel, or Team start projection, including skipped gates without classifier invocation. The existing `model`, `routed_category`, `routed_model`, and `routing_reason` fields remain authoritative for final behavior. Mecatui and the existing `InspectSession` delegation view expose the same persisted evidence without changing configuration, classifier defaults, breaker policy, or model authority. Team-tool correlation reuses `SessionRelationship.CallID`, populated through private supervisor wiring and matched exactly to the parent Team tool call; it adds no public constructor or relationship field, leaves direct teams unchanged, and treats historical missing-call relationships as loadable but uncorrelated.

## Interface contract

- **gRPC / protobuf:** Add `RoutingDecision`: `backend`, `classifier_model`, `candidate_category`, and `candidate_model` at 1-4; optional doubles `confidence = 5` and `minimum_confidence = 6`; `outcome = 7`; `consecutive_misses = 8`; `miss_limit = 9`; and `breaker_open = 10`. Nest it at `Subagent = 19`, `TeamMemberSpec = 9`, and `Parallel = 26`. Preserve existing fields, generated camel-case JSON, generated Go/TypeScript, and handwritten TypeScript events.
- **Exported Go APIs / interfaces:** Replace the callback tuple with these exact pre-v1 types:

  ```go
  type ModelRouteResult struct {
      Category   string
      Model      string
      Usage      session.Usage
      Reason     string
      OK         bool
      Confidence *float64
  }

  type SubagentModelRouter struct {
      Backend           string
      ClassifierModel   string
      MinimumConfidence *float64
      Route             func(context.Context, string) ModelRouteResult
  }
  ```

  `Deps.SubagentModelRouter` becomes `*SubagentModelRouter`. Candidate fields include below-threshold values and do not claim execution. Jev alone reports validated confidence and a non-nil zero threshold; LLM leaves both absent. This pre-v1 breaking change has no bridge or registry. Add exported misses `RouterMissTimeout`, `RouterMissLowConfidence`, `RouterMissInputOverLimit`, and `RouterMissCapacityTimeout` with values `timeout`, `low-confidence`, `input-over-limit`, and `capacity-timeout`; update API snapshots and the changelog.

  Add `RoutingDecision` with fields `Backend`, `ClassifierModel`, `CandidateCategory`, `CandidateModel`, `Confidence *float64`, `MinimumConfidence *float64`, `Outcome`, `ConsecutiveMisses int`, `MissLimit int`, and `BreakerOpen bool`. Add immutable pointers to all three delegation payloads. Reuse `SessionRelationship.CallID` via private Team-tool supervisor wiring; add no public field or constructor. Add the five Jev values plus `TypesafeAPIKey` to app config, the four Jev settings to permconfig, and default category plus maximum bytes to Jev options.
- **Tool schemas:** Delegation and `InspectSession` arguments stay unchanged. Add `actual_model`, `routed_category`, `routed_model`, `routing_reason`, and optional `routing_decision` to the persisted-event-backed `delegation` response in snake case. Do not read prompts, cache session state, or infer historical evidence.
- **CLI / config:** Add strict operator-only `models.router.backend` (`llm|jev`, default `llm`) and `jev.{model,base-url,minimum-confidence,maximum-input-bytes}`. Defaults are `jev-1.13.0`, the SDK endpoint, `0`, and `16384`; confidence must be finite in `[0,1]`, and the byte limit must be an integer in `[1,65536]`. `TYPESAFE_API_KEY` is the only credential and is not YAML-configurable. Strict decoding always applies. Disabled or taxonomy-free routing skips client construction and active credential/connectivity/conflict checks. Active Jev rejects a missing key or explicit LLM-only `classifier-slot`; active LLM rejects a `jev` block. `default-category` works with either backend. Endpoints and credentials never enable Jev. Add no CLI flag, prompt schema, forced fallback, or startup default-category validation.

- **Events / persistence:** Validate candidates and aliases before projection. Rejected candidates never set accepted-route fields; factory rejection keeps candidates but uses inherited `model` and `route-target-unavailable`. Snapshot the unchanged breaker after classification; only classifier misses increment it. Configured routers always emit a decision, while absent and historical evidence stays nil. Existing relays/stores persist it. Team-tool rows require exact root/incarnation, team/member, parent `CallID`, kind, edge, owner, retained state, and uniqueness; private wiring supplies `CallID`. Historical missing-call and direct-team relationships remain loadable but uncorrelated. Deleted, stale, ambiguous, cross-owner, or different-call evidence fails closed. Add no lifecycle, cache, or storage resource.
- **Security / authority:** Operator selection consents to Typesafe egress; Jev is not an authorization or safety classifier. Scrub `TYPESAFE_API_KEY`, disable redirects, permit HTTPS/loopback HTTP, and enforce bounds. One sanitizer repairs and bounds evidence, omits invalid scores, and excludes content, secrets, endpoints, raw responses/errors, service identity, and raw member identity. Preserve owner and child-content isolation.
- **Compatibility / migration:** LLM remains default; observed LLM deadlines refine from `cancelled` to `timeout`. `default-category` keeps its advisory behavior. Existing fields remain unchanged, optional evidence preserves historical absence, and historical Team relationships without `CallID` load but do not correlate; direct teams are unchanged. Pin SDK v0.1.0 separately from model `jev-1.13.0`. Update owning user, generated-config, architecture, implementation-note, TUI, compatibility, and resource-inventory docs without hand-editing generated Markdown.

## In scope - 7 scenarios, in implementation order

The original 16 acceptance criteria and proof names remain. Scenarios 5-7 add three compact end-to-end proofs.

### Scenario 1 - Operator selects one router backend

The strict operator configuration owns the backend decision, while taxonomy presence and the existing kill-switch retain the enable model in [ADR 0042](../adr/0042-taxonomy-gated-model-router.md).

**Acceptance:**
- AC1.1: configuration with no `backend` continues to build the existing LLM classifier, and `backend: jev` builds the Jev adapter only when routing is enabled.
  - verify: `TestADR_0350_Scenario1_BackendSelection`
- AC1.2: strict parsing rejects unknown keys, wrong shapes, an unknown backend, invalid confidence values, and `maximum-input-bytes` values that are non-integers, below 1, or above 65536, regardless of `disabled` or taxonomy presence. Disabled or taxonomy-free routing prevents client construction and skips only inactive-backend credential, connectivity, and selection-conflict checks. An active Jev router rejects a missing credential and an explicitly authored `classifier-slot`; an active LLM router rejects a `jev` block. `default-category` is valid for both backends.
  - verify: `TestADR_0350_Scenario1_ValidationAndDisabledPrecedence`
- AC1.3: project-tier router configuration cannot select Jev, and endpoint or credential presence alone does not enable or select it.
  - verify: `TestADR_0350_Scenario1_OperatorAuthority`

### Scenario 2 - Jev makes one validated category decision

Composition sends the task as untrusted `SystemOneRequest.State` and asks one `Choice` question whose trusted criteria map category names to descriptions. Both the LLM and Jev instructions ask for the category that best matches required expertise, specialty, and reasoning difficulty. They state that read-only review or investigation is not necessarily trivial, require explicit specialty criteria to be honored without hard-coded security/model category names, and treat task text as classification data rather than routing instructions. Jev's API-specific wrapper uses the same principles. The chosen category still resolves through the shared alias/category mapping, preserving [ADR 0031](../adr/0031-subagent-model-router.md), [ADR 0034](../adr/0034-team-parallel-model-routing.md), and [ADR 0066](../adr/0066-route-unpinned-and-writable-delegations.md).

**Acceptance:**
- AC2.1: a valid exact Jev choice routes each eligible Subagent invocation, an unpinned named specialist, and a writable explorer through the shared router. Team members route once at `AddMember` and retain that model for the member lifetime; each Parallel branch routes once for that branch. Explicit per-call or definition pins, including explicit `model: inherit`, as well as fork and resume bypass classification but retain configured-router skip metadata.
  - verify: `TestADR_0350_Scenario2_ExistingRoutingSemantics`
- AC2.2: malformed or invalid protocol responses return `bad-verdict`; an observed offered-set violation returns `unknown-category` when the SDK exposes it as structured data; transport or SDK errors return `classifier-error`. The adapter never parses arbitrary error text to invent a cause, and unknown local status fails closed to `classifier-error`. Every outcome falls back to the ordinary inherited model with no accepted routed category.
  - verify: `TestADR_0350_Scenario2_InvalidResponseFallsBack`
- AC2.3: when `minimum-confidence` is nonzero, confidence below the threshold is `low-confidence`; a local input/category cap is `input-over-limit`; an expired queue while the caller remains active is `capacity-timeout`. Each is a miss for the existing three-miss per-run breaker and does not force `default-category`. A below-threshold candidate that resolves to a model different from the inherited fallback proves that `model` names the inherited model that actually ran while `routed_model` remains absent; candidate evidence never masquerades as an accepted route.
  - verify: `TestADR_0350_Scenario2_LowConfidenceAbstains`
- AC2.4: composition exhaustively maps Jev's adapter-local typed statuses to the engine's shared constants: local transport/SDK error -> `classifier-error`, invalid protocol -> `bad-verdict`, structured observed unknown category -> `unknown-category`, low confidence -> `low-confidence`, local cap -> `input-over-limit`, active-caller queue expiry -> `capacity-timeout`, caller cancellation -> `cancelled`, and caller/request/SDK deadline -> `timeout`. The existing LLM classifier applies the same cancellation/deadline distinction without changing timeout durations or call count; no signal means no guessed timeout. The adapter does not import `engine/agent`.
  - verify: `TestADR_0350_Scenario2_BackendNeutralOutcomes`
- AC2.5: a real-composition LLM router and the actual SDK-backed Jev adapter accept the current user-shaped medium `default-category` configuration and carry the capability-oriented contract in their rendered instructions. The optional trimmed hint is exactly `If no category clearly fits, choose %q.`; absent/blank values add nothing, and quote-escaping expansion participates in the configured byte count over the complete rendered request. The hint neither widens the offered set nor changes confidence, protocol-error, or inherited-model fallback behavior. Request-capture tests prove hostile task text appears only in Jev `State`, never in trusted instructions or criteria, and the criteria remain exactly the operator-authored category names and descriptions.
  - verify: `TestADR_0350_Scenario2_DefaultCategoryHint`

Failure precedence is based on observed terminal state, not a wall-clock race. A successfully obtained and validated result is processed normally as a hit, `low-confidence`, or local mapping miss and retains its usage; late unrelated cancellation never relabels it. For a failed classification, retain independently validated SDK `ProtocolError.Usage` first, then classify an observed caller `ctx.Err()` (`context.Canceled` -> `cancelled`, `context.DeadlineExceeded` -> `timeout`); then an observed request/operation-context deadline, typed `errors.Is(err, context.DeadlineExceeded)`, or typesafe `ErrAttemptTimeout` -> `timeout`; then `errors.Is(err, context.Canceled)` -> `cancelled`; then structured invalid protocol -> `bad-verdict`; otherwise -> `classifier-error`. Queue expiry is `capacity-timeout` only while the caller context remains active. Tests pin observed state rather than a wall-clock winner. The LLM reference classifier uses structured terminal signals only and never parses raw provider text.

### Scenario 3 - Usage survives success and failure without invention

The existing dispatch seam in `engine/agent/dispatch.go` (`routeTaskBody`) records classifier usage, as required by [ADR 0031](../adr/0031-subagent-model-router.md). The adapter preserves Jev's independently validated usage rather than creating another accounting path.

**Acceptance:**
- AC3.1: successful responses map Jev input and output tokens to `session.Usage` and return them with the route hit. An otherwise valid response rejected for low confidence or local category-to-model mapping returns the same usage with the miss; a response that completes at a context-classification race also retains valid usage. The existing parent fold records it exactly once. Every nonhit continues to count toward the existing three-miss per-run router breaker rather than any backend-health circuit breaker.
  - verify: `TestADR_0350_Scenario3_HitUsage`
- AC3.2: a `ProtocolError` with non-nil validated usage returns that usage with the miss, including reported zero. Nil or absent usage maps to the callback's existing zero value as "not reported," not proof that the service spent zero tokens; no spend is estimated and no unknown-usage field is added.
  - verify: `TestADR_0350_Scenario3_ErrorUsage`
- AC3.3: the shared and per-session engine paths use the same backend builder and category alias lookup, so provider-fixed child construction and no-FS behavior do not diverge.
  - verify: `TestADR_0350_Scenario3_CompositionParity`

### Scenario 4 - External routing is bounded and secret-safe

The external call follows the command-credential boundary in [AGENTS.md](../../AGENTS.md) and the router's fail-soft behavior in [ADR 0031](../adr/0031-subagent-model-router.md). The local request limit is byte-measurable, not a claim about Jev tokenization or service context acceptance.

**Acceptance:**
- AC4.1: the adapter permits at most eight concurrent requests across one `app.Build`. The shared engine and all per-session engines contend for the same real semaphore and client rather than receiving independent eight-slot pools. The adapter gives up after a 10-second queue wait, applies a 10-second request deadline and 1 MiB response limit, configures zero SDK retries, disables redirects, and rejects non-loopback cleartext endpoints before sending data. Caller cancellation releases capacity and sends/cancels at most one request. An active-caller queue expiry is `capacity-timeout`; caller cancellation is `cancelled`; a caller deadline, request deadline, or SDK attempt timeout is `timeout`. Validated response and `ProtocolError` usage are retained. Every timeout/cancellation remains fail-soft and no path duplicates a classification call.
  - verify: `TestADR_0350_Scenario4_BoundedTransport`
- AC4.2: before SDK marshalling, the adapter measures UTF-8 bytes of the complete rendered textual request: delegated task/state, the full trusted instructions including the `%q`-expanded optional `default-category` hint, selected model, fixed question identifier and question text, and every category name and description. A sum above configured `maximum-input-bytes`, whose default is 16384 and whose accepted range is 1 through the immutable 65536-byte ceiling, or more than 255 categories returns the inherited `input-over-limit` fallback without inference or I/O. The adapter never truncates the task, instructions, or taxonomy. The request uses no user-defined `MarshalJSON`. This byte count is a local safety policy, not a vendor tokenization, context-window, or service-acceptance guarantee.
  - verify: `TestADR_0350_Scenario4_RequestLimits`
- AC4.3: the 1 MiB cap bounds response data and request deadlines bound transport waiting. A valid response whose encoded body exceeds the cap is rejected through the ordinary fail-soft classifier fallback, proving the cap is enforced independently of malformed-response handling. The plan claims no hard end-to-end wall-clock limit over arbitrary CPU work performed by SDK JSON processing.
  - verify: `TestADR_0350_Scenario4_BoundedTransport`
- AC4.4: `TYPESAFE_API_KEY` cannot enter any agent-facing command environment. Diagnostics and evidence contain only sanitized configured/candidate metadata, breaker state, and shared canonical reasons, never credentials, task text, category descriptions, raw answers, probabilities, request IDs, endpoints, raw response models, errors, or response bodies. Cross-backend outcomes use identical canonical values. Optional zero confidence/threshold remain distinguishable from absence, while NaN/Inf/out-of-range/custom hostile data cannot break JSON, protobuf, logs, or terminal rendering. The existing diagnostics owners emit the same bounded facts and one build-time effective backend/classifier/threshold/category-mapping summary without per-turn duplicate log lines or service-version claims.
  - verify: `TestADR_0350_Scenario4_SecretAndContentRedaction`, `TestADR_0350_Scenario4_CanonicalOutcomeProjection`
- AC4.5: offline tests use ordinary injected clients and fake or `httptest` endpoints, perform no live Jev call or private payload upload, and require no new testing framework.
  - verify: inspection - test fixtures and CI configuration contain no live Typesafe endpoint invocation

### Scenario 5 - One shared decision explains routing and fallback

The additive evidence extends the existing bounded start-event contract in [ADR 0083](../adr/0083-routing-reason-on-delegation-start.md), the [live model-routing architecture](../architecture/providers.md#the-semantic-model-router-phase-5), and the target-bound debugger described in [the TUI guide](../tui.md#debug-a-stored-session).

**Acceptance:**
- AC5.1: hits, low-confidence candidates, mapping misses, target-factory rejection, classifier failures, breaker skips, pins, fork, resume, unavailable delegation families, and nil `Route` produce the exact candidate/final/outcome and post-decision breaker semantics in this plan. A configured router emits an independent sanitized `RoutingDecision` without extra classifier calls; an absent router or historical payload remains nil. Existing `model`, `routed_category`, `routed_model`, and `routing_reason` remain the sole authority for the actual model, accepted route, and final reason.
  - verify: `TestADR_0350_Scenario5_DecisionEvidence`

### Scenario 6 - Wire replay and debugger preserve the decision

The evidence follows the three existing lifecycle families from [ADR 0083](../adr/0083-routing-reason-on-delegation-start.md), the [live model-routing architecture](../architecture/providers.md#the-semantic-model-router-phase-5), and the debugger boundary in [the TUI guide](../tui.md#debug-a-stored-session). Exact Team parent-call correlation is the security-review refinement required to make AC6.1's retained-lineage rule fail closed; it adds no user-visible routing capability.

**Acceptance:**
- AC6.1: the exact protobuf fields and optional-presence semantics round-trip through generated Go/TypeScript and handwritten SDK events. Proof starts from a real router decision producer, crosses the existing Subagent, Parallel, or Team family emitter, passes through a real relay into the durable event log, reloads that event, and reaches `InspectSession delegation`; a shared real-relay proof may rely on the existing per-family emitter proofs rather than duplicate the whole stack three times. Historical absence stays absent. Both gRPC and HTTP transport proofs cover a Team roster decision whose confidence and minimum threshold are present zero values, plus a historical Team roster whose optional decision or optional numbers are absent, and the handwritten TypeScript SDK preserves the same distinction.
  Team roster recovery uses the existing lineage graph. A Team-tool member must have a `SessionRelationship.CallID` exactly equal to the `team.start` parent call ID, plus the exact root session and incarnation, team ID, member name, kind, edge, owner, current retained state, and existing uniqueness defense. Private supervisor wiring records the parent Team tool-call ID without adding a public constructor or field. A substitution regression runs two real Team tool calls and proves that a member from one call cannot satisfy the other call's roster. Historical team-member relationships with no `CallID` still load but produce no correlated row; direct-team relationships and behavior stay unchanged. Missing exact correlation, deleted or replaced lifetimes, zero, ambiguity, stale lineage, cross-owner data, and different-call data fail closed with no row and never fall back to the tuple alone. Tests prove private IDs are omitted from wire, SDK, client, UI, and debugger JSON; scoped opaque existing handles appear only after proof. Tests also prove no child-content or hostile/secret metadata leak. An unavailable event log uses the debugger's existing unavailable/error behavior; a missing or failed event append leaves routing evidence absent and never reconstructed by inference or falsely labeled as a skipped classifier. Append failures retain the existing warn-and-continue run behavior, with offline failure-injection coverage.
  - verify: `TestADR_0350_Scenario6_WireAndDebugger`

### Scenario 7 - A user can diagnose the route end to end

This journey follows the bounded event contract in [ADR 0083](../adr/0083-routing-reason-on-delegation-start.md), starts at the existing compact delegation card in the [live model-routing architecture](../architecture/providers.md#the-semantic-model-router-phase-5), continues through the F6 Agents details, and ends in the target-bound debugger documented in [the TUI guide](../tui.md#debug-a-stored-session).

**Acceptance:**
- AC7.1: mecatui keeps the current compact one- or two-line model status and adds an optional candidate/confidence-below-threshold cue on fallback. The expanded existing card and F6 focus show backend, configured classifier, candidate, actual model, decision reason, threshold, and breaker snapshot. Parallel branch completion and group completion retain the start decision on those existing compact and F6 surfaces; they do not add an inline routing card or a new lifecycle event. Nil LLM confidence displays as unavailable in detail, never zero; historical nil keeps the old label. Viewport, narrow-width, width-zero, all three delegation-family subviews, client mapping, and selectively updated goldens cover the new fields. `user-docs/features/choose-models.md` gives the workflow card -> F6 Agents details -> `mecatui debug` / `InspectSession delegation`, separates effective configuration from runtime outcome, distinguishes rejected candidate from actual model, explains that confidence is not accuracy and thresholds require a labeled workload, and proposes no nonexistent evaluation command or magic threshold.
  - verify: `TestADR_0350_Scenario7_UserJourney`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Guardrails, escape checking, approval reviewers, and security-model taxonomy | Separate contracts | They do not reuse or change this router in this plan. Security may appear only as an optional user-defined category example. |
| Main-session, cross-provider, or per-turn routing | Separate architectural work | Jev only chooses a category for currently eligible delegated work. |
| Router caches, backend-health breakers, and changed confidence/breaker defaults | Later work | Every eligible decision remains live; this plan observes the current run-scoped breaker. |
| Evaluation commands, accuracy metrics, latency/cost evidence, probability vectors, request IDs, and raw service versions | Later observability/evaluation work | V1 exposes only the common bounded decision needed to debug model selection. |
| New storage backends or a fourth delegation lifecycle | Not planned | Existing events, relays, stores, and three delegation families carry the optional decision. |
| Token-accounting hardening | Independent work | Preserve reported Jev usage through the existing result without making unrelated accounting changes an adoption condition. |

## Definition of done

1. `task lint`, `task test`, `task docs`, `task api:check`, and required generated-contract/SDK checks pass; all tests remain offline.
2. `task ac-trace-strict` resolves all 19 acceptance criteria when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. The implementation updates the owning public guides, generated configuration source/output, architecture, implementation notes, TUI reference, engine compatibility artifacts, and cloud-native resource inventory.
5. The implementation PR links Plan / Interface PR #1735 and this amendment commit, records the direct conversational approval and continuing stacked waiver, and does not claim the plan merged.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Jev receives delegated task text. Explicit operator selection is consent to this egress, and user guidance must make the boundary clear.
- Category descriptions are operator-controlled criteria, but task state can contain prompt injection. Exact choice validation bounds the result; it does not make Jev an authorization, safety, or accuracy oracle.
- Confidence is a backend-native signal, not measured accuracy. Operators need a representative labeled workload to calibrate a nonzero threshold; this plan chooses no universal threshold.
- SDK and service-model versions, service limits, and model names can change independently. The implementation pins SDK v0.1.0 separately from service model `jev-1.13.0`; the configurable input guard defaults to 16384 bytes and cannot exceed its immutable 64 KiB ceiling, while the 255-category guard remains a local bound rather than a calibrated service default.
