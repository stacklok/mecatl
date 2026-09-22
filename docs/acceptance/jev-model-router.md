# Jev delegated-model router - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural - selecting an external decision service and exposing durable routing-decision evidence change the operator configuration, exported engine API, wire, persistence, credential, data-egress, and adapter boundaries of delegated-model routing.
**Decision record:** [ADR 0350](../adr/0350-jev-delegated-model-router.md)
**Phase:** delegated-model router backend
**Status:** in-progress, 2026-09-22 — implementation is proceeding under direct operator approval and the continuing explicit stacked waiver. Plan / Interface PR [#1735](https://github.com/stacklok/mecatl/pull/1735) remains open and retains its human merge gate; this does not claim that it merged.
**Delivery:** Split. The external decision boundary and exact operator, engine, wire, and evidence contracts are directly approved for this stacked implementation under the continuing waiver.
**Expected tasks:** deferred to orchestration
**Approved baseline:** `3cb18c618ce87e9c5ee7aef5c9200f2422f3e78b`, approved directly by the operator for stacked implementation. Latest amended plan commit: `3cb18c618ce87e9c5ee7aef5c9200f2422f3e78b`. Original/prior plan provenance: `299801397112cf9a2cc5a1221e180b6acb5c7e1d` / `14923277ec157b299913a66e2be6304671b0be2c`.

This plan adds Jev as an explicitly selected classifier backend for the existing semantic model router and makes each routing decision explainable through the existing delegation lifecycle. It preserves the current taxonomy, category-to-model mapping, same-provider child construction, precedence, fallback, and per-run breaker described in [ADR 0031](../adr/0031-subagent-model-router.md) and [ADR 0042](../adr/0042-taxonomy-gated-model-router.md).

The proposal uses `typesafe-go` v0.1.0 with Jev model `jev-1.13.0`. Jev is a bounded decision adapter, not an LLM provider: it does not enter the provider registry, implement `port.LLMProvider`, route main-session turns, or change provider selection.

## Human decisions

- [x] Keep this contract router-only - Decision: guardrails, escape checking, approval reviewers, cross-provider or main-session routing, per-turn routing, caches, new evaluation commands, metrics, storage backends, latency/cost/probability-vector evidence, and token-accounting hardening are outside this plan. Existing reported router usage continues through the current accounting seam.
- [x] Approve the operator surface - Decision: `backend: llm|jev` defaults to `llm`; Jev has `model`, `base-url`, and `minimum-confidence`; `TYPESAFE_API_KEY` is its only credential source; an explicitly authored LLM-only `classifier-slot` conflicts with an active `backend: jev`; and disabled or taxonomy-free routing skips inactive-backend credential, connectivity, and selection-conflict checks while strict YAML/schema validation still applies.
- [x] Approve the shared `default-category` advisory hint - Decision: the existing common routing policy is accepted for both LLM and Jev. A non-empty trimmed category appends `If no category clearly fits, choose %q.` to classifier instructions; absent, empty, or whitespace-only values add no hint. It never widens the offered category set or becomes an automatic runtime fallback.
- [x] Approve abstention behavior - Decision: an absent or zero `minimum-confidence` disables confidence filtering; a valid answer below an explicitly configured threshold is a router miss that inherits the normal child model and counts toward the existing breaker, rather than selecting `default-category`.
- [x] Approve the external-call policy - Decision: one shared client and eight-slot semaphore per `app.Build`, a 10-second queue wait, a 10-second request deadline, a 1 MiB response limit, a proposed 64 KiB locally measured UTF-8 text-input cap, at most 255 categories, no SDK retries, no redirects, HTTPS except loopback test/development endpoints, and explicit documentation that delegated task text leaves the deployment when Jev is selected.
- [x] Approve the backend-neutral router outcome contract - Decision: the engine owns the shared closed miss taxonomy; Jev returns only an adapter-local typed status and composition exhaustively maps it before the engine callback. The approved mapping and evidence requirements are recorded below; unshipped `jev-*` values are replaced rather than migrated on the wire.
- [x] Approve capability-oriented classification and shared decision evidence - Decision: both backends classify by required expertise, specialty, and reasoning difficulty while treating task text as data rather than routing instructions. Every configured-router decision gets one bounded `RoutingDecision` snapshot on the existing Subagent, Parallel, or Team start projection, including skipped gates without classifier invocation. The existing `model`, `routed_category`, `routed_model`, and `routing_reason` fields remain authoritative for final behavior. Mecatui and the existing `InspectSession` delegation view expose the same persisted evidence without changing configuration, classifier defaults, breaker policy, or model authority.

## Interface contract

- **gRPC / protobuf:** Add `RoutingDecision` with fields `backend = 1`, `classifier_model = 2`, `candidate_category = 3`, `candidate_model = 4`, `optional double confidence = 5`, `optional double minimum_confidence = 6`, `outcome = 7`, `int32 consecutive_misses = 8`, `int32 miss_limit = 9`, and `bool breaker_open = 10`. Add optional nested fields at the verified next free numbers: `Subagent.routing_decision = 19`, `TeamMemberSpec.routing_decision = 9`, and `Parallel.routing_decision = 26`. Existing field numbers and meanings remain unchanged. Protobuf JSON uses the generated camel-case names. Regenerate Go and TypeScript contracts and map the nested message through the handwritten TypeScript event interfaces.
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

  Change `agent.Deps.SubagentModelRouter` from the old function to `*SubagentModelRouter`. `Category` and `Model` are the validated candidate, including a below-threshold candidate; they do not prove that the target ran. `Confidence` is present only for a validated backend-native signal. The LLM backend leaves it nil and never invents a score. The wrapper makes configured backend, configured/defaulted classifier model, and threshold available on skipped calls without invoking `Route`; Jev preserves an explicitly configured zero threshold as a non-nil pointer, while LLM uses nil. This is an intentional Changed, pre-v1 breaking Go API update with no compatibility callback framework, generic classifier port, or registry. Add exactly four exported common-outcome constants: `RouterMissTimeout = "timeout"`, `RouterMissLowConfidence = "low-confidence"`, `RouterMissInputOverLimit = "input-over-limit"`, and `RouterMissCapacityTimeout = "capacity-timeout"`. Update `engine/api/*.txt` and `engine/CHANGELOG.md` under `engine/COMPATIBILITY.md`.

  Add the exact domain value:

  ```go
  type RoutingDecision struct {
      Backend           string
      ClassifierModel   string
      CandidateCategory string
      CandidateModel    string
      Confidence        *float64
      MinimumConfidence *float64
      Outcome           string
      ConsecutiveMisses int
      MissLimit         int
      BreakerOpen       bool
  }
  ```

  `Backend` is the closed configured origin `llm|jev`; `ClassifierModel` is the configured/defaulted model and never claims a raw service-returned version. `Outcome` is the closed set `routed|fallback|skipped`. Add `*RoutingDecision` to `session.SubagentPayload`, `session.ParallelPayload`, and `session.TeamMemberSpec`. Each event receives an independent immutable copy, not a shared mutable pointer. Add trusted log-only `MemberSessionID session.SessionID` and `MemberIncarnation session.IncarnationID` to `session.TeamMemberSpec` alongside `RoutingDecision`. These are intentional Added exported engine API fields and snapshot updates, but are omitted from normal protobuf, HTTP, TypeScript/SDK, client, UI, and debugger JSON projections. After enrollment, `TeamTool` reads the actual member session ID and incarnation through a same-package private `Supervisor` accessor and stamps that true identity; it never derives either value from `MemberSessionID` formatting. Host composition adds `internal/app.Config.RouterBackend`, `RouterJevModel`, `RouterJevBaseURL`, `RouterJevMinimumConfidence`, and `TypesafeAPIKey`; its existing `RouterDefaultCategory` passes to Jev. `internal/adapter/permconfig.RouterSection` gains `Backend` and `Jev *JevRouterSection` with `Model`, `BaseURL`, and `MinimumConfidence`. The internal Jev `Options` gains `DefaultCategory string`.
- **Tool schemas:** The Subagent, Team, Parallel, and `InspectSession` argument schemas remain unchanged. Extend the existing `InspectSession` `delegation` response DTO with `actual_model`, `routed_category`, `routed_model`, `routing_reason`, and optional `routing_decision`, using snake-case JSON. It reads the already persisted start events; it does not read raw prompts, add a session-state cache, or infer missing historical evidence.
- **CLI / config:** Extend the strict, operator-tier-only `models.router` mapping with the following keys. `backend` accepts `llm` or `jev` and defaults to `llm`. `jev.model` defaults to `jev-1.13.0`; `jev.base-url` is optional and otherwise uses the pinned SDK's official endpoint; `jev.minimum-confidence` defaults to `0` (filter disabled) and must always be finite and in `[0,1]`. `TYPESAFE_API_KEY` supplies the credential and is not representable in YAML. A non-empty taxonomy still enables routing, and `disabled: true` still wins. Strict decoding and schema validation always run, even when routing is disabled or has no taxonomy. Only active-backend checks are skipped when disabled or taxonomy-free. When active, `backend: jev` conflicts with an explicitly authored `classifier-slot`; inherited slot defaults do not conflict. The existing `default-category` is accepted by either backend as an optional advisory instruction. An active `backend: llm` conflicts with a `jev` block. Selecting active Jev without a non-empty credential is an error. An endpoint or credential alone never enables Jev. No new CLI flag, prompt-config schema, startup default-category validation, or forced fallback is proposed.

  ```yaml
  models:
    router:
      backend: jev
      jev:
        model: jev-1.13.0
        # base-url is normally omitted
        minimum-confidence: 0
      default-category: medium
      categories:
        - name: fast
          description: Small, well-scoped changes
          model: cheap
        - name: deep
          description: Cross-cutting design and difficult debugging
          model: capable
  ```
- **Events / persistence:** Candidate data is validated and model aliases are resolved locally before projection, even when confidence rejects the candidate. A mapping failure retains `CandidateCategory` and leaves `CandidateModel` empty. A child-engine factory rejection retains both candidates, records `Outcome: "fallback"`, keeps the existing `routing_reason: "route-target-unavailable"`, and leaves `model` as the inherited model that actually ran. On accepted routes, only the existing `routed_category` and `routed_model` assert the route, while `model` remains the actual model. Do not duplicate those final facts in `RoutingDecision`.

  Capture the breaker snapshot after the classifier decision: a hit resets consecutive misses to zero; a miss increments them; a breaker skip reports its latched snapshot; and pin, fork, resume, unavailable family, or nil-`Route` skips do not increment it. A classifier hit followed by target-factory rejection keeps the existing post-hit breaker snapshot and does not invent another miss. Do not change the existing breaker threshold, confidence default, or breaker policy. A configured router always emits a `RoutingDecision`, including gate skips with no classifier call. No configured router, an old event, or a historical payload without the field remains nil with no fabricated backend, outcome, score, or threshold.

  Persist the nested value through the existing event relay and stores. Team roster evidence remains on `team.start`; repair the currently unreachable debugger projection only by an exact trusted child-lifetime join using the roster's `MemberSessionID` and `MemberIncarnation`, then validate parent call, team ID, member name, kind, edge, owner, current retained state, and the existing uniqueness defense. Missing correlation, a deleted or replaced lifetime, or zero, ambiguous, stale, or cross-owner evidence fails closed with no row; never fall back to `(team_id, member_name)` alone. Public views use existing scoped opaque handles only after proof and never expose a raw member session ID or incarnation. No fourth delegation lifecycle, backend wire field outside `RoutingDecision`, storage backend, cache, or session-storage resource is introduced.
- **Security / authority:** Only operator-tier `settings.yaml` may select Jev. The delegated task is untrusted state sent to Typesafe, while category names and descriptions are trusted operator criteria. Jev is not an authorization or safety classifier. Add `TYPESAFE_API_KEY` to `envscrub.DenyExact` and `envscrub.NonOverridableExact`. Disable redirects, permit HTTPS and loopback HTTP only, and enforce the request/response bounds.

  Apply one evidence sanitizer before every projection. It repairs UTF-8, removes controls, and bounds configured identifiers, candidates, and closed reasons consistently with existing delegation metadata. It rejects or omits non-finite or out-of-range confidence and threshold values so a custom callback cannot break JSON or protobuf. Evidence never contains task text, category descriptions, raw service responses, probability vectors, request IDs, endpoints, keys, transport errors, raw SDK-returned model identity, raw member session IDs, or incarnations. Tests plant secret-shaped, hostile Unicode/control data and non-finite values. Existing owner isolation and child-content boundaries remain in force.
- **Compatibility / migration:** Existing LLM configuration remains the default. Its observed deadline outcome refines from `cancelled` to `timeout`; consumers must accept the documented common reason. `default-category` keeps its LLM advisory behavior and becomes available to unshipped Jev configuration as the same advisory hint. Existing Go/JSON fields remain unchanged; new nested evidence is optional, and historical nil renders the old labels without invented data. Pin `github.com/stacklok/typesafe-go` v0.1.0 separately from service model `jev-1.13.0`. Implementation updates `user-docs/features/choose-models.md`, generated configuration sources, applicable deployment credential pages, `docs/architecture/providers.md`, `docs/design/IMPLEMENTATION-NOTES.md`, `docs/tui.md`, and the cloud-native resource inventory. Generated Markdown is not hand-edited.

## In scope - 7 scenarios, in implementation order

The original 16 acceptance criteria and proof names remain. Scenarios 5-7 add three compact end-to-end proofs.

### Scenario 1 - Operator selects one router backend

The strict operator configuration owns the backend decision, while taxonomy presence and the existing kill-switch retain the enable model in [ADR 0042](../adr/0042-taxonomy-gated-model-router.md).

**Acceptance:**
- AC1.1: configuration with no `backend` continues to build the existing LLM classifier, and `backend: jev` builds the Jev adapter only when routing is enabled.
  - verify: `TestADR_0350_Scenario1_BackendSelection`
- AC1.2: strict parsing rejects unknown keys, wrong shapes, an unknown backend, and invalid confidence values regardless of `disabled` or taxonomy presence. Disabled or taxonomy-free routing prevents client construction and skips only inactive-backend credential, connectivity, and selection-conflict checks. An active Jev router rejects a missing credential and an explicitly authored `classifier-slot`; an active LLM router rejects a `jev` block. `default-category` is valid for both backends.
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
- AC2.3: when `minimum-confidence` is nonzero, confidence below the threshold is `low-confidence`; a local input/category cap is `input-over-limit`; an expired queue while the caller remains active is `capacity-timeout`. Each is a miss for the existing three-miss per-run breaker and does not force `default-category`.
  - verify: `TestADR_0350_Scenario2_LowConfidenceAbstains`
- AC2.4: composition exhaustively maps Jev's adapter-local typed statuses to the engine's shared constants: local transport/SDK error -> `classifier-error`, invalid protocol -> `bad-verdict`, structured observed unknown category -> `unknown-category`, low confidence -> `low-confidence`, local cap -> `input-over-limit`, active-caller queue expiry -> `capacity-timeout`, caller cancellation -> `cancelled`, and caller/request/SDK deadline -> `timeout`. The existing LLM classifier applies the same cancellation/deadline distinction without changing timeout durations or call count; no signal means no guessed timeout. The adapter does not import `engine/agent`.
  - verify: `TestADR_0350_Scenario2_BackendNeutralOutcomes`
- AC2.5: a real-composition LLM router and the actual SDK-backed Jev adapter accept the current user-shaped medium `default-category` configuration and carry the capability-oriented contract in their rendered instructions. The optional trimmed hint is exactly `If no category clearly fits, choose %q.`; absent/blank values add nothing, and quote-escaping expansion participates in the 64 KiB count over the actual rendered instructions. The hint neither widens the offered set nor changes confidence, protocol-error, or inherited-model fallback behavior. Request-capture tests prove hostile task text appears only in Jev `State`, never in trusted instructions or criteria, and the criteria remain exactly the operator-authored category names and descriptions.
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
- AC4.1: the adapter permits at most eight concurrent requests per Build, gives up after a 10-second queue wait, applies a 10-second request deadline and 1 MiB response limit, configures zero SDK retries, disables redirects, and rejects non-loopback cleartext endpoints before sending data. Caller cancellation releases capacity and sends/cancels at most one request. An active-caller queue expiry is `capacity-timeout`; caller cancellation is `cancelled`; a caller deadline, request deadline, or SDK attempt timeout is `timeout`. Validated response and `ProtocolError` usage are retained. Every timeout/cancellation remains fail-soft and no path duplicates a classification call.
  - verify: `TestADR_0350_Scenario4_BoundedTransport`
- AC4.2: before SDK marshalling, the adapter measures UTF-8 bytes of the actual rendered Jev instructions and request data: fixed capability-oriented text, the optional `default-category` hint after `%q` expansion, task, selected model, fixed question identifier, and every category name and description. A sum over 64 KiB or more than 255 categories returns `input-over-limit` without I/O; the adapter never truncates the task or taxonomy. The request uses no user-defined `MarshalJSON`. The limits are local guards, not tokenizer or service-acceptance guarantees.
  - verify: `TestADR_0350_Scenario4_RequestLimits`
- AC4.3: the 1 MiB cap bounds response data and request deadlines bound transport waiting, but the plan claims no hard end-to-end wall-clock limit over arbitrary CPU work performed by SDK JSON processing.
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

The evidence follows the three existing lifecycle families from [ADR 0083](../adr/0083-routing-reason-on-delegation-start.md), the [live model-routing architecture](../architecture/providers.md#the-semantic-model-router-phase-5), and the debugger boundary in [the TUI guide](../tui.md#debug-a-stored-session). The trusted lifetime fields are a security-review refinement required to make existing AC6.1's fail-closed retained-lineage rule sound; they add no user-visible routing capability.

**Acceptance:**
- AC6.1: the exact protobuf fields and optional-presence semantics round-trip through generated Go/TypeScript and handwritten SDK events. Real persisted Subagent, Parallel, and Team start events replay the same decision through `InspectSession delegation`; historical absence stays absent. Team roster recovery uses its exact trusted `MemberSessionID` and `MemberIncarnation` child-lifetime correlation, then validates the parent call, team ID, member name, kind, edge, owner, current retained state, and existing uniqueness defense. A regression persists an old `team.start`, deletes or expires its child, then creates a same-owner replacement with the same `(team_id, member_name)` and a new incarnation; the old roster is not bound to that replacement. Missing exact correlation, deleted or replaced lifetimes, zero, ambiguity, stale lineage, and cross-owner data fail closed with no row and never fall back to the tuple alone. Tests prove private IDs are omitted from wire, SDK, client, UI, and debugger JSON; scoped opaque existing handles appear only after proof. Tests also prove no child-content or hostile/secret metadata leak. An unavailable event log uses the debugger's existing unavailable/error behavior; a missing or failed event append leaves routing evidence absent and never reconstructed by inference or falsely labeled as a skipped classifier. Append failures retain the existing warn-and-continue run behavior, with offline failure-injection coverage.
  - verify: `TestADR_0350_Scenario6_WireAndDebugger`

### Scenario 7 - A user can diagnose the route end to end

This journey follows the bounded event contract in [ADR 0083](../adr/0083-routing-reason-on-delegation-start.md), starts at the existing compact delegation card in the [live model-routing architecture](../architecture/providers.md#the-semantic-model-router-phase-5), continues through the F6 Agents details, and ends in the target-bound debugger documented in [the TUI guide](../tui.md#debug-a-stored-session).

**Acceptance:**
- AC7.1: mecatui keeps the current compact one- or two-line model status and adds an optional candidate/confidence-below-threshold cue on fallback. The expanded existing card and F6 focus show backend, configured classifier, candidate, actual model, decision reason, threshold, and breaker snapshot. Nil LLM confidence displays as unavailable in detail, never zero; historical nil keeps the old label. Viewport, narrow-width, width-zero, all three delegation-family subviews, client mapping, and selectively updated goldens cover the new fields. `user-docs/features/choose-models.md` gives the workflow card -> F6 Agents details -> `mecatui debug` / `InspectSession delegation`, separates effective configuration from runtime outcome, distinguishes rejected candidate from actual model, explains that confidence is not accuracy and thresholds require a labeled workload, and proposes no nonexistent evaluation command or magic threshold.
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
- SDK and service-model versions, service limits, and model names can change independently. The implementation pins SDK v0.1.0 separately from service model `jev-1.13.0`; the 64 KiB and 255-category guards remain local bounds rather than calibrated service defaults.
