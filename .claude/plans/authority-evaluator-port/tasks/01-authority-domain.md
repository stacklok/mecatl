---
id: 01-authority-domain
title: Pure authority value and neutral evaluator port
blocked_by: []
status: done
branch: "plan-authority-evaluator-port/01-authority-domain"
worktree: ""
issue: "371"
retries: 0
last_error: "task test baseline blocker: cmd/mecatui learning-settings tests reject macOS /var symlink"
accumulator: acc/authority-evaluator-port
---

# Task brief

Create the one in-tree authority representation in `engine/governance`: a plain
capability-set value containing tool names, remaining delegation depth, and the
execution-posture flags retained by the acceptance plan. Implement only
monotone-downward operations, especially `Narrow` and the erroring one-hop
operation. Add the provider-neutral `port.AuthorityEvaluator` contract and
request/principal types, with no Cedar types and no serialization/parser.
Provide the core-facing no-op and local set-check implementations in appropriate
adapters plus a shared conformance suite if that can be done without pulling in
session or composition. Do not wire dispatch, session persistence, delegation,
or flags; later tasks own them.

## Acceptance criteria

- AC1.1: `Narrow` returns the set intersection of tool names, the lower of the two delegation depths, and the conjunction of each execution-posture flag; the result is never a superset of either input on any axis.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario1_NarrowIsIntersectionOnEveryAxis`
- AC1.2: Every operation on a capability set is monotone downward — for any two valid inputs, each input contains the result. No union, widening, or additive operation exists.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario1_OperationsAreMonotone`, `FuzzAuthorityOperationsAreMonotone`
- AC1.3: Consuming a delegation hop at remaining depth zero is an error, not a silent pass or a clamp.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario1_DepthExhaustionIsAnError`
- AC1.4: A capability set has exactly one in-tree representation and exactly one place that serializes it; no second parser, canonical form, or field-count check exists in any package.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario1_SingleRepresentationAndSerializer`
- AC3.2: The request carries the derived set, the tool name, the delegation depth, and a principal comprising definition, instance, and owner; it carries no raw tool arguments, credentials, or Cedar-specific types.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_RequestShapeIsNeutralAndCarriesTheSet`
