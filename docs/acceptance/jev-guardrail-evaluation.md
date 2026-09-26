# Jev guardrail evaluation - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural - a decision service evaluating tool actions and results changes security and external-data boundaries even before it gains enforcement authority.
**Decision record:** [ADR 0368](../adr/0368-evidence-before-jev-guardrail-authority.md)
**Phase:** evidence for an optional Jev guardrail stage; no runtime checker changes
**Status:** draft, 2026-09-26. The operator authorized a local plan draft and a non-shipping PoC. Live calls on synthetic cases were separately authorized for that PoC; this plan grants no ongoing production-data disclosure or checker authority.
**Delivery:** Split. Security authority and external disclosure require a separately reviewed contract before runtime integration.
**Expected tasks:** deferred to orchestration
**Plan PR:** draft, not an approval event
**Approved baseline:** absent until approved

Establish whether Jev can usefully reduce contextual action and inbound review cost without silently clearing an attacker-controlled action or result. This plan owns the evaluation contract, not an implementation of a new guardrail backend. [ADR 0352](../adr/0352-jev-delegated-model-router.md) covers delegated model routing only. [ADR 0363](../adr/0363-contextual-investigative-guardrails.md) describes the single contextual reviewer and its separate action, inbound, and permission jobs. The existing [quality harness](../../internal/adapter/guardraileval/eval.go) distinguishes offline protocol proofs from authorized live efficacy measurements.

[PR #1730](https://github.com/stacklok/mecatl/pull/1730) proposes a named `permissionMode` vocabulary and checker-or-explicit-off admission for `auto` and `yolo`; it is open as of this draft. It also changes the separate headless child ask-reviewer default. Recheck its merged contract before designing production admission. Neither `models.router.backend: jev` nor `TYPESAFE_API_KEY` enables a guardrail checker. Runtime settings, if warranted, belong to a later acceptance contract.

## Human decisions

- [x] Separate research from enforcement. - Decision: the operator approved an evaluation-first plan and a non-shipping PoC; no Jev result is authorized to allow a tool, release a result, or approve a child ask by this plan.
- [ ] Decide whether any runtime shadow observation is necessary after the synthetic comparison. A shadow path would need its own data-disclosure, root-run ownership, terminal cancellation, and accounting contract.
- [ ] Approve versioned Jev question rubrics and a representative, independently labeled held-out corpus. Specify the risk slices, how full provenance and bounded evidence are represented, and what constitutes an unsupported case rather than claiming that a text-only fixture represents an actual review.
- [ ] Before a formal billable comparison, approve the exact Jev and LLM routes, synthetic data scope, maximum spend, and numerical safety/false-positive/latency/cost acceptance criteria. The existing operator permission for a small PoC does not select these broader gates.
- [ ] Resolve the ADR number used by PR #1730 against the current main branch: its draft ADR 0365 collides with the local microVM ADR 0365. Recheck the PR head before treating its plan as an approved baseline.
- [ ] Decide whether the proposed auxiliary-usage contract is merged before a later runtime design. If not, separately review exact attribution, error usage, and public API changes rather than silently charging Jev to main-run usage.

## Interface contract

- **gRPC / protobuf:** None - evaluation only; no service method, message, or field changes. Any later runtime metadata requires another contract.
- **Exported Go APIs / interfaces:** None - no `engine/agent.ToolReviewer`, `port.LLMProvider`, or other exported engine change. The evaluation harness remains in `internal/adapter/guardraileval` and may use internal types.
- **Tool schemas:** None - no tool or result is offered to the working model by this evaluation.
- **CLI / config:** None - no production `guardrails.jev` option or `--permission-mode` change. Evaluation uses explicit local synthetic inputs and an explicit, separately authorized live route. The router configuration remains independent.
- **Events / persistence:** None - no session event, snapshot, or replay change. An evaluation report is an offline artifact containing only aggregate measures, corpus/rubric/model version IDs, sample counts, and known reported usage; it never retains reviewed content or credentials.
- **Security / authority:** The evaluation never changes actions, asks, result release, or guardrail health. Only synthetic, reviewed inputs may be sent to externally authorized routes. Full review inputs require an explicit egress decision; a field omitted for privacy or length is an unsupported case, not a cleared action. Default secret scrubbing and bounded read authority remain in force.
- **Compatibility / migration:** Existing LLM guardrails, Jev routing, operator config, and historical sessions remain unchanged. A later fast path would require an explicit amendment to the contextual reviewer contract and cannot inherit this evaluation's approval.

## In scope - 3 scenarios, in implementation order

### Scenario 1 - Representative inputs retain the review boundary

The [current corpus](../../internal/app/testdata/contextual_guardrails_corpus.v1.json) contains short text pairs, while a real [review request](../../engine/agent/review.go) also carries the effective call, principal facts, evidence availability, caller, and task trajectory. [ADR 0363](../adr/0363-contextual-investigative-guardrails.md) binds acceptable outcomes to complete decision-relevant context. TypeSafe's [Jev 1.13 limitations](https://docs.typesafe.ai/model-jaggedness/jev-1.13.md) identify adversarial steering and irrelevant context as accuracy risks.

**Acceptance:**
- AC1.1: a versioned synthetic evaluation input distinguishes action and inbound cases, records an independently adjudicated expected outcome, and pairs benign issue instructions, admitted project guidance, and quoted attacks with concrete adversarial redirections.
  - verify: `TestJevGuardrailEvaluation_Scenario1_PairedContextualCorpus`
- AC1.2: a case lacking decision-relevant effective content, provenance, trajectory, or readable authorized evidence is reported as unsupported or incomplete; it cannot become a candidate fast clearance by dropping a field or truncating text.
  - verify: `TestJevGuardrailEvaluation_Scenario1_IncompleteCannotClear`
- AC1.3: permission review, path-escape decisions, and headless child ask-review are identified as distinct workloads; evaluation output cannot claim authority for them.
  - verify: `TestJevGuardrailEvaluation_Scenario1_DistinctJobs`

### Scenario 2 - A Jev experiment cannot enforce or silently spend

The [Jev router](../../internal/adapter/jevrouter/jevrouter.go) demonstrates bounded SDK use, but its fail-soft model fallback under [ADR 0352](../adr/0352-jev-delegated-model-router.md) is unsuitable as an authorization rule. The [existing evaluation gate](../../internal/adapter/guardraileval/eval.go) rejects live evaluation without an explicit route and spend approval.

**Acceptance:**
- AC2.1: offline protocol mode makes no external request; a live experiment requires explicit route, synthetic-data, and spend consent, and never reads ambient API credentials or operator project state as corpus input.
  - verify: `TestADR_0368_Scenario2_ExplicitLiveAdmission`
- AC2.2: Jev's rendered input, concurrent requests, queue wait, response, and deadline are bounded; missing answers, nonfinite values, over-limit input, cancellation, and transport failure return a measured miss, never a candidate clearance or an executable permission decision.
  - verify: `TestJevGuardrailEvaluation_Scenario2_BoundedMisses`
- AC2.3: key values, tool arguments, provider response bodies, and corpus text are absent from durable diagnostics and aggregate reports; a credential-file option is used only at execution and is never stored in a test fixture.
  - verify: `TestJevGuardrailEvaluation_Scenario2_ReportRedaction`

### Scenario 3 - Measurements support a genuine go/no-go choice

The existing [contextual plan](contextual-guardrails.md) and [ADR 0363](../adr/0363-contextual-investigative-guardrails.md) require paired false-warning, false-block, and missed-attack measurement. The LLM comparator is another model, not a ground-truth label.

**Acceptance:**
- AC3.1: the report distinguishes protocol-only runs from authorized live results and records sample counts, unsafe clearance candidates, misses, false warnings/blocks, abstention and estimated fallback rates, latency distributions, known token usage, and unknown cost without fabricating efficacy or zero spend from absent provider usage.
  - verify: `TestJevGuardrailEvaluation_Scenario3_ReportTruthfulness`
- AC3.2: a held-out comparison records model IDs and question-rubric versions; choosing a threshold on tuning cases does not turn those same cases into held-out evidence, and every unqualified input is conservatively counted as LLM fallback in the simulated cascade.
  - verify: `TestJevGuardrailEvaluation_Scenario3_HeldOutAndFallback`
- AC3.3: the evaluation ends in a documented no-go, a separately reviewed runtime-cascade proposal, or a specifically justified shadow proposal. No evaluation result alone modifies `ToolReviewer`, permission policy, or result withholding.
  - verify: inspection of the evaluation report and the subsequent human decision - no runtime behavior is produced by this plan

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Jev-only guardrail backend, fast-acceptance or human approval | Separate reviewed runtime contract | A typed score cannot meet the existing investigative evidence and source-rationale obligations by itself. |
| Production shadowing or dual-provider calls on real content | Post-results human decision | Even advisory shadowing is a new external-data and lifecycle boundary. |
| Child ask-reviewer, path-escape check, permission job, model routing changes | Separate contracts | These are distinct authority or routing decisions. |
| Live production-data evaluation and open-ended provider spend | Separate explicit authorization | This plan grants neither. |

## Definition of done

1. Every human decision needed to mark this plan `proposed` is recorded and the plan checker, its regression fixtures, and `task docs` pass.
2. Offline evaluator and adapter tests exercise the named proof scenarios. No live API call is part of the ordinary test suite.
3. Any live efficacy report has independently authorized routes and spend. Protocol-only results never claim security quality.
4. The Plan / Interface PR is reviewed and merged before a non-spike implementation; any runtime integration gets its own reviewed contract and gates.

## Deferred decisions and known risks

Jev's Noul probability and Choice/Score confidence describe different quantities. Neither is a measured probability that a guardrail decision is correct. The TypeSafe request can carry many narrow questions, but complete source-aware review may require evidence the classifier cannot inspect without another authorized disclosure. Keep such cases in the fallback population when measuring candidate coverage.
