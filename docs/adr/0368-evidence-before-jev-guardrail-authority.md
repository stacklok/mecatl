# ADR 0368 - Require evidence before Jev can clear guardrail reviews

- Status: Proposed
- Date: 2026-09-26
- Scope: external decision services evaluated for contextual action and inbound guardrails
- Extends: ADR 0363's one contextual reviewer and ADR 0352's separate Jev router boundary; does not supersede either
- Superseded by: none

## Context

Mecatl's contextual guardrail reviewer sees the exact effective action or inbound result, harness-established task provenance, current-root trajectory, and narrowly authorized evidence. It has separate action, inbound, and worker permission jobs. It can investigate a version-bound evidence handle before deciding, and an enforcing inbound check can withhold an already-produced result. [ADR 0363](./0363-contextual-investigative-guardrails.md) describes those commitments.

Mecatl also uses Jev for delegated-model routing. The existing [router decision](./0352-jev-delegated-model-router.md) is an explicit, fail-soft category selection. That result may choose a child model, but it does not authorize an action or release tool output. Jev is a typed decision service, not a chat model, and its router adapter cannot satisfy the investigative review contract merely by changing the question.

TypeSafe's [Jev 1.13 limitations](https://docs.typesafe.ai/model-jaggedness/jev-1.13.md) explicitly identify adversarial steering, irrelevant context, and multi-step indirection as weak spots. Its [confidence guide](https://docs.typesafe.ai/confidence.md) distinguishes the distribution statistic on Choice and Score from the Noul probability. The [guardrails cookbook](https://docs.typesafe.ai/cookbooks/llm_guardrails.md) shows a battery of atomic questions on short messages; it does not establish quality on Mecatl's tool actions, source provenance, or whole results. Existing offline fixtures demonstrate protocol wiring, not live model efficacy.

Selecting Jev for guardrail input would create a different disclosure boundary from the router: effective tool arguments, results, principal facts, and authorized evidence may leave the harness. A customer-data no-training commitment is not the same as a default zero-retention guarantee. Fallback to a generative investigator also spends time and tokens and must name an admitted provider instead of silently substituting one.

## Proposed decision

Evaluate a possible Jev guardrail stage against versioned, independently labeled, paired synthetic review cases before granting it runtime authority. The evaluation must distinguish action from inbound, include harness provenance and evidence completeness, and separately measure misses, false warnings and blocks, potential fast-clear coverage, latency, and spend. It must make an incomplete or undisclosable input an unsupported case, not a classifier clearance. Live comparison requires separately approved routes, synthetic data, spend, and quality bars. An offline protocol proof is never evidence of detection quality.

Keep the existing LLM investigator and deterministic gates authoritative during this evaluation. The independent Jev router is not a checker declaration for `auto`, including under the proposed unified permission-mode [plan in PR #1730](https://github.com/stacklok/mecatl/pull/1730). Do not register Jev as a fake LLM provider, infer consent from a key or model-router setting, copy the router's fail-soft fallback into guardrail enforcement, or use a distribution confidence value as a universal safety threshold.

Only a separately reviewed production contract may allow Jev to clear an action or release a result. That contract must specify exact eligible inputs, affirmative completeness of all decision-relevant content, permitted external disclosure, fallback on uncertainty and errors, one overall deadline, per-provider accounting, and status that distinguishes authoritative decisions from candidates. The permission job, path-escape policy, and child ask-reviewer need their own authority decisions. A production shadow service likewise requires a reviewed ownership, cancellation, resource, and egress contract; it is not a prerequisite for synthetic evaluation.

The [evaluation acceptance plan](../acceptance/jev-guardrail-evaluation.md) carries the scenarios and unresolved human decisions. This proposed ADR records the architectural boundary, not a claim that any Jev guardrail capability has shipped or passed empirical validation.

## Consequences

The evaluation can disprove the economic or security case without imposing an unused external checker on operators. It may also reveal a small eligible workload for a later Jev-to-LLM cascade. The costs are a labeled corpus, separately authorized live comparison, and a second contract gate before a production fast path. A Jev-only promise remains unavailable without evidence and a new security-authority decision.

## See also

- [Hooks and guardrails](../architecture/hooks-and-guardrails.md) describes the current enforcement path.
- [Contextual investigative guardrails](../acceptance/contextual-guardrails.md) owns the existing review contract.
- [Purpose-attributed auxiliary token usage](../acceptance/auxiliary-token-usage.md) is a proposed accounting dependency for any later runtime integration.
