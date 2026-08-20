---
id: 16-ownerless-authority-composition
title: Preserve ordinary ownerless sessions while Cedar requires identity
blocked_by: [14-authority-evaluator-presence]
status: done
branch: plan-authority-evaluator-port/16-ownerless-authority-composition
worktree: ""
issue: "371"
retries: 0
last_error: ""
accumulator: acc/authority-evaluator-port
---

# Repair brief

Review-2 blocker: caller identity is optional under ADR 0204. A bound authority must not make ordinary unauthenticated `app.Build` sessions unusable. Preserve exact owner identities when supplied, but require an owner only when the selected authority evaluator needs one (Cedar); local and noop evaluators must authorize ownerless sessions. Prove the real composition path works ownerless and Cedar fails closed when identity is absent. Revert the known-bad tool-less request shaping for a missing evaluator: keep execution fail-closed, but leave request shaping as authority filtering.

## Acceptance criteria

- AC3.2: The execution chokepoint consults the selected evaluator once per bound tool call and fails closed when the evaluator is unavailable; request shaping is never relied on for enforcement.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_EveryDispatchPathConsultsTheEvaluatorOnce`, `TestADR_0233_AuthorityEvaluator_BoundSessionWithoutEvaluatorFailsClosed`
- AC6.1: Composition selects the configured evaluator and mints a root authority that cannot be widened by child delegation.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario6_MintPopulatesEveryFieldExplicitly`
- AC7.1: Cedar may apply additional operator policy denials without granting a capability absent from the carried set.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario7_CedarCanDenyWithoutWidening`
- Repair proof: identity remains optional for local/noop `app.Build` sessions and required for Cedar evaluation.
  - verify: `TestADR_0233_AuthorityEvaluator_OwnerlessCompositionUsesLocalEvaluator`, `TestADR_0233_AuthorityEvaluator_OwnerlessCedarSessionFailsClosed`
