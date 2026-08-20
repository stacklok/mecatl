---
id: 07-cedar-adapter
title: Add opt-in static Cedar authority adapter
blocked_by: [05-delegation-derivation, 06-resource-attribute]
status: done
branch: "plan-authority-evaluator-port/07-cedar-adapter"
worktree: ""
issue: "371"
retries: 0
last_error: "task test baseline blockers: macOS /var symlink checks and skills lifecycle workspace test; task docs cannot fetch matlatl"
accumulator: acc/authority-evaluator-port
---

# Task brief

Implement the optional Cedar AuthorityEvaluator entirely under `internal/`.
Load static operator-owned policy at startup and make a policy-load failure fatal
when Cedar is selected. Cedar can tighten a carried set but cannot grant a tool
omitted from it; provide a shipped guard/lint rejecting definition-group grants.
Use the resource descriptor derived by task 06 for a path-subtree deny rule,
without generated policy text or per-subagent registrations. Register the
adapter in composition behind an explicit operator-facing selector. Keep the
engine module dependency closure unchanged and make the shared evaluator
conformance suite pass.

## Acceptance criteria

- AC7.1: The shipped policy set is static and contains no generated text; per-call variation rides request-scoped entity attributes derived from the carried set, and nothing is registered or removed per subagent.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario7_PolicyIsStaticAndDataIsPerRequest`
- AC7.2: An operator rule can deny a capability the carried set permits — including confining a definition to a path subtree — and cannot grant one the carried set omits.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario7_OperatorRuleTightensButCannotGrant`
- AC7.3: An entity hierarchy linking an instance to its definition is used only for tightening; a policy granting a capability to a definition group is rejected by a shipped lint or guarded test, because Cedar membership widens.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario7_DefinitionGroupGrantIsRejected`
- AC7.4: The Cedar dependency appears only in the adapter under `internal/`; the engine module's dependency closure is unchanged and its standalone build still passes.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario7_CedarStaysOutOfTheEngineModule`
- AC7.5: The adapter is off by default and selected by an explicit flag; a policy set that fails to load is a startup failure, not a silent fallback to permit.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario7_PolicyLoadFailureIsFatal`
