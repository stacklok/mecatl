# ADR 0350 - Jev as an explicit delegated-model router backend

- Status: Accepted by direct operator approval under the continuing explicit stacked waiver; this does not claim that Plan / Interface PR #1735 merged
- Date: 2026-09-22
- Scope: operator-tier `models.router` configuration, root composition, a new internal Typesafe/Jev adapter, the shared engine router result and miss taxonomy, and bounded routing-decision evidence on the existing delegation event, client, and debugger paths
- Extends: [ADR 0031](./0031-subagent-model-router.md) with an explicitly selected non-LLM classifier backend, a backend-neutral result, and shared decision evidence
- Supersedes: ADR 0031 only where it requires a composition-built one-turn LLM classifier, uses the callback tuple replaced below, or treats an observed deadline as `cancelled`; ADR 0031's taxonomy mapping, routing precedence, fallback, same-provider construction, and breaker policy otherwise remain in force
- Superseded by: none

## Context

The semantic model router currently spends one tool-less LLM turn to choose an operator-defined category for eligible delegated work. Composition maps that category to a same-provider model and passes it through the existing child-engine factories. [ADR 0042](./0042-taxonomy-gated-model-router.md) makes the operator taxonomy the enable decision and retains an explicit kill-switch.

Jev is a narrower decision service. The verified `typesafe-go` v0.1.0 API accepts untrusted state plus a trusted `Choice` criterion map and returns one exact choice, probabilities, confidence, model identity, request identity, and token usage. It is not a chat or generative provider. Treating it as `port.LLMProvider` would add a false provider identity and expose unrelated provider behavior.

The existing callback tuple reports only accepted routes and a final miss reason. That is enough to run the child, but not enough to explain a low-confidence candidate, identify which configured classifier was used, distinguish absence from a real zero threshold, or show the breaker state that caused a skip. ADR 0083 exposes final routing reasons on the three existing delegation-start families, but it deliberately does not carry the common decision evidence needed by a live UI or a later debugger read.

Selecting Jev also creates a new external-data and credential boundary. Delegated task text leaves the Mecatl deployment, `TYPESAFE_API_KEY` becomes a harness credential, and a shared client plus concurrency limiter outlive one classification call. These are operator and system-boundary decisions.

## Decision

Allow exactly two delegated-router backends: `llm` and `jev`. Keep `llm` as the default. Select Jev only through operator-tier `models.router.backend: jev`; endpoint, model, and credential presence never selects or enables it. Taxonomy presence and `disabled: true` retain ADR 0042's enable and kill-switch semantics.

Both backends classify the delegated task by the expertise, specialty, and reasoning difficulty required to complete it. The common prompt states that read-only review or investigation is not necessarily trivial, honors explicit operator specialty criteria without hard-coded security or model category names, and treats task text as classification data rather than routing instructions. Jev applies the same principles in its API-specific trusted instructions while sending the task only as untrusted state. The optional common `default-category` policy is available to both backends: a non-empty trimmed value appends `If no category clearly fits, choose %q.` to trusted instructions. It neither widens the offered set nor becomes an automatic runtime fallback. The fully rendered instructions, including quote expansion, count toward the configured local input bound.

Replace the callback tuple with the minimum typed engine contract:

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

`Deps.SubagentModelRouter` becomes `*SubagentModelRouter`. `Category` and `Model` are the locally validated candidate, including a candidate rejected by confidence; they are not proof that a child used that model. `Confidence` is present only for a validated backend-native signal. The LLM backend leaves it nil and never invents a score. `MinimumConfidence` preserves configured presence: Jev's explicit/defaulted zero is a non-nil pointer, while LLM uses nil. The wrapper's backend, configured/defaulted classifier model, and threshold remain available when a gate skips `Route`. A nil `Route` is a safe skip. This is an intentional Changed pre-v1 engine API update with no old/new callback bridge, generic classifier port, or classifier registry.

Keep category-to-model resolution in composition through `lookupModelAlias`. Routing eligibility, precedence, same-provider child construction, no-FS behavior, and the three-consecutive-miss per-run breaker do not vary by backend. The common classifier-outcome taxonomy is the existing `degenerate-input`, `classifier-error`, `cancelled`, `bad-verdict`, and `unknown-category`, plus `timeout`, `low-confidence`, `input-over-limit`, and `capacity-timeout`. `session.RoutingReason*` remains the separate final event-reason gate. Every nonhit continues to count against the existing breaker. No confidence default or breaker policy changes.

Implement Jev as a root internal adapter, not an engine adapter or provider module. It issues one `SystemOne` request whose untrusted `State` is the delegated task and whose fixed-identifier `Choice` maps category names to trusted operator descriptions. It accepts only an exact offered choice and a fully valid response. It returns a small adapter-local typed status and does not import `engine/agent`. Composition exhaustively maps transport/SDK error to `classifier-error`, invalid protocol to `bad-verdict`, structured offered-set violation to `unknown-category`, low confidence to `low-confidence`, local cap to `input-over-limit`, active-caller queue expiry to `capacity-timeout`, caller cancellation to `cancelled`, and caller/request/SDK deadline to `timeout`. Unknown local status maps to `classifier-error`; arbitrary error text is never parsed. The LLM classifier makes the same cancellation/deadline distinction without changing timeout duration or call count.

Failure precedence is based on observed terminal state. A successfully validated result is processed as a hit, low-confidence candidate, or local mapping miss and retains usage; late unrelated cancellation does not relabel it. For a failed classification, preserve independently validated `ProtocolError.Usage`, then inspect caller context, request/operation deadline signals, typed SDK attempt timeout, typed cancellation, structured protocol failure, and finally generic classifier failure in that order. Queue expiry is `capacity-timeout` only while the caller remains active.

Add a domain value that describes one bounded configured-router decision:

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

`Backend` is the configured closed origin `llm|jev`. `ClassifierModel` is the configured/defaulted classifier model, not a claim about an SDK-returned service version. `Outcome` is `routed`, `fallback`, or `skipped`. Optional pointers distinguish absence from zero. Add `*RoutingDecision` to `session.SubagentPayload`, `session.ParallelPayload`, and `session.TeamMemberSpec`; each event gets an independent immutable copy.

Team-tool roster correlation reuses the existing exported `session.SessionRelationship.CallID`; it does not add fields to `TeamMemberSpec` or introduce another public constructor or option. Private wiring from `TeamTool` to `Supervisor` supplies the exact parent Team tool-call ID when member sessions are created. The debugger accepts a roster row only when a retained member's relationship has that exact call ID and also matches the parent session and incarnation, team ID, member name, kind, edge, owner, current retained state, and existing uniqueness checks. Two real Team calls therefore cannot substitute members for one another even when other metadata overlaps. Historical team-member relationships without `CallID` remain valid snapshots but cannot satisfy this correlation. Direct teams continue to create and use their existing empty parent-call relationship.

Validate the candidate and resolve its model locally before projection, including below-threshold responses. Mapping failure retains the candidate category and leaves candidate model empty. A low-confidence or otherwise rejected candidate never populates accepted-route fields: the existing payload `model` remains the inherited model that actually ran, even when it differs from `CandidateModel`, and `routed_model` stays absent. A target-factory rejection retains both candidates, records `fallback`, and uses existing final reason `route-target-unavailable`; `model` again remains the inherited model actually used. Existing `routed_category` and `routed_model` remain accepted-route-only. Do not duplicate final facts in the new value.

Capture breaker state after the classifier decision. A hit resets misses to zero, a miss increments them, and a breaker skip reports its latched state. Pin, fork, resume, unavailable family, and nil-`Route` skips do not increment it. A classifier hit followed by factory rejection retains the post-hit snapshot and does not create an extra miss. A configured router emits skip metadata without invoking the classifier. No configured router, old events, and historical payloads without the field remain nil; no consumer reconstructs backend, outcome, confidence, or threshold.

Add one optional nested protobuf message with exact fields:

```proto
message RoutingDecision {
  string backend = 1;
  string classifier_model = 2;
  string candidate_category = 3;
  string candidate_model = 4;
  optional double confidence = 5;
  optional double minimum_confidence = 6;
  string outcome = 7;
  int32 consecutive_misses = 8;
  int32 miss_limit = 9;
  bool breaker_open = 10;
}
```

Use the verified next free fields: `Subagent.routing_decision = 19`, `TeamMemberSpec.routing_decision = 9`, and `Parallel.routing_decision = 26`. Preserve all existing fields and meanings. Generate Go and TypeScript contracts and map the nested value through handwritten TypeScript event interfaces. Protobuf JSON remains generated camel case.

Persist this value through the existing event relay and stores. Extend the existing target-bound `InspectSession` `delegation` row with `actual_model`, `routed_category`, `routed_model`, `routing_reason`, and optional `routing_decision` in snake-case JSON. It reads typed persisted start events and never caches raw session state or infers historical evidence. Proof begins with a real routing producer, crosses an existing family emitter and a real relay into durable storage, reloads the event, and reaches `InspectSession`; a shared relay proof may compose with existing per-family emitter proofs. The currently unreachable Team start projection joins a roster member only when the retained member's relationship exactly matches the event's parent Team call ID and all other trusted lineage fields described above. Missing `CallID`, a deleted or replaced lifetime, ambiguity, stale data, a foreign owner, or a member from another Team call fails closed with no row. It never falls back to `(team_id, member_name)` alone and never exposes a raw member session ID or incarnation.

Project only bounded, control-scrubbed, UTF-8-valid metadata. Validate confidence and threshold as finite values in `[0,1]`; omit invalid custom-callback numbers so JSON and protobuf remain valid. Never project task text, category descriptions, raw responses, probability vectors, request IDs, endpoints, keys, errors, or raw SDK-returned model identity. Existing payload `model` and `routing_reason` remain authoritative for actual execution and final reason. Diagnostics use the same bounded facts through existing owners, plus one build-time effective backend/classifier/threshold/category-mapping summary. They do not add per-turn duplicate lines or claim an unknown service version.

Mecatui keeps its compact model status. On fallback it may add a candidate/confidence-below-threshold cue. Existing expanded cards and F6 details show configured backend/classifier, candidate, actual model, final reason, threshold, and breaker snapshot. Parallel branch and group completion retain the start decision on those existing compact and F6 surfaces rather than creating an inline routing card or lifecycle event. Missing LLM confidence displays as unavailable in detailed views, never as zero; historical nil keeps the prior label. Both gRPC and HTTP preserve present-zero confidence and threshold values on Team roster decisions while keeping historical absence absent, including in the handwritten TypeScript SDK. The existing session debugger supplies durable evidence after the run. Public guidance leads operators from the card to F6 and then to `mecatui debug` / `InspectSession delegation`, distinguishes effective configuration from runtime decision, and states that confidence is not accuracy. Threshold calibration requires a representative labeled workload; this decision introduces no evaluation command or universal threshold.

Extend strict configuration with `backend` and `jev.{model,base-url,minimum-confidence,maximum-input-bytes}`. Jev model defaults to `jev-1.13.0`; confidence defaults to explicit zero and must be finite in `[0,1]`; `maximum-input-bytes` defaults to 16384 and must be an integer in `[1,65536]`. The 65536-byte maximum is an immutable local ceiling. `TYPESAFE_API_KEY` is the only credential and is not representable in YAML. Strict shape validation always applies. Disabled or taxonomy-free routing skips client construction, credential/connectivity checks, and active-backend conflicts. Active Jev rejects a missing credential and an explicitly authored LLM-only `classifier-slot`; implicit slot defaults do not conflict. Active LLM rejects a Jev block. `default-category` is valid for both backends. No new CLI flag or prompt-config schema is added.

Pin `github.com/stacklok/typesafe-go` v0.1.0 separately from service model `jev-1.13.0`. Configure one client and one real eight-slot semaphore per `app.Build`; the shared engine and every per-session engine use that same capacity. Apply a 10-second queue wait, a 10-second request deadline, zero SDK retries, disabled redirects, HTTPS or loopback HTTP, and a 1 MiB response limit. A valid response whose encoded body exceeds the limit must take the ordinary fail-soft fallback, not only malformed oversized responses. Before SDK marshalling, measure UTF-8 bytes of the complete rendered textual request: delegated task/state, full trusted instructions including the `%q`-expanded default-category hint, selected model, fixed question identifier and question text, and every category name and description. Reject a sum above configured `maximum-input-bytes` or more than 255 categories without inference or I/O, and never truncate. The default is 16384 bytes and configuration can tighten or raise it only through 65536 bytes. These are local safety bounds, not vendor tokenization, context-window, service-acceptance, or wall-clock guarantees.

Preserve independently validated usage exactly once through the existing parent fold, including low-confidence and local mapping misses and valid `ProtocolError.Usage`. Nil usage remains "not reported" rather than proof of zero. Add no latency/cost field or new accounting path.

Treat task state and the service response as sensitive producer-influenced data. Add `TYPESAFE_API_KEY` to exact environment deny and non-overridable sets. Selecting Jev is operator consent to delegated-task egress, which user documentation must state. Add the Build-owned client and semaphore to the cloud-native resource inventory; neither has durable session state. Do not add a cache, backend-health breaker, new storage backend, or live Jev test.

The linked acceptance plan records direct conversational approval of the original correction and this 2026-09-23 amendment. This ADR is accepted by that direct approval under the continuing explicit stacked waiver, while the plan remains `proposed` and the implementation remains a candidate until human merge. Plan / Interface PR #1735 and stacked implementation PR #1738 remain open with human merge gates; direct approval does not claim either PR merged.

## Consequences

Operators can select a purpose-built decision model without presenting it as a chat provider. Existing installations remain on LLM classification unless they select Jev. Both backends provide one common, bounded explanation of candidate, outcome, final model, and breaker state across live events and durable debugger evidence.

The engine API change is intentionally breaking before v1. `ModelRouteResult`/`SubagentModelRouter` replace the prior callback, and `RoutingDecision` adds optional evidence fields to the existing delegation payloads. Team correlation reuses the existing `SessionRelationship.CallID`; it adds no exported constructor or relationship field. The wire change is additive and optional. Older and historical events remain valid and render without fabricated decision data, while historical team relationships without a call ID remain loadable but uncorrelated. Existing actual-model and final-reason fields retain authority, avoiding conflicting truth between old and new clients.

Jev adds one dependency, credential, egress path, and bounded shared resource. Classification remains fail-soft, so an outage preserves delegation availability but loses routing quality. Confidence is observable but is not an accuracy metric; operators must calibrate a threshold against their own labeled workload.

No result cache means repeated eligible tasks make repeated calls. Shared fixed concurrency and request/response limits bound the controlled path without claiming a hard SDK CPU bound. Operators can tune the input limit within the fixed 1..65536-byte policy range; changing the range or other constants requires ordinary review.

## See also

- [Jev delegated-model router acceptance plan](../acceptance/jev-model-router.md)
- [ADR 0031](./0031-subagent-model-router.md) for routing precedence, fallback, and breaker behavior
- [ADR 0034](./0034-team-parallel-model-routing.md) for route-once member and branch behavior
- [ADR 0035](./0035-per-delegation-model-surface.md) for the authoritative actual-model field
- [ADR 0042](./0042-taxonomy-gated-model-router.md) for taxonomy enablement and the kill-switch
- [ADR 0066](./0066-route-unpinned-and-writable-delegations.md) for unpinned/writable routing and explicit pins
- [ADR 0083](./0083-routing-reason-on-delegation-start.md) for the authoritative final routing reason
- [Provider architecture](../architecture/providers.md#the-semantic-model-router-phase-5) for current routing behavior
- [Session debugger](../architecture.md) for the durable `InspectSession` boundary
- [ADR 0027](./0027-cloud-native.md) for the resource-inventory convention
