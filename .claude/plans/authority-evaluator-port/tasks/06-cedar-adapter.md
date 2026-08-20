---
id: 06-resource-attribute
title: Derive a neutral resource attribute at the execution boundary
blocked_by: [03-execution-evaluator, 05-delegation-derivation]
status: done
branch: "plan-authority-evaluator-port/06-resource-attribute"
worktree: ""
issue: "371"
retries: 0
last_error: "task test baseline blockers: macOS /var symlink checks and skills lifecycle workspace test; task docs cannot fetch matlatl"
accumulator: acc/authority-evaluator-port
---

# Task brief

Add a provider-neutral, non-secret resource descriptor to
`port.AuthorityRequest` so an evaluator can distinguish a filesystem target
without receiving raw tool arguments. At the `execute` chokepoint, derive it
only for tools whose bounded argument shape identifies a local resource; reject
malformed or ambiguous paths fail-closed. The descriptor must be normalized and
constrained to the session workspace identity where applicable, and it must not
contain raw JSON, credentials, or Cedar types. Existing exact tool-name checks
remain intact. Update the request-shape proof to establish that resource data is
derived at the boundary rather than forwarded as arguments. Do not implement any
Cedar policy in this task.

## Acceptance criteria

- The evaluator request exposes a normalized resource descriptor only when the execution boundary can derive one from a recognized tool call; it never exposes raw tool-call arguments.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario7_ResourceAttributeIsDerivedWithoutRawArguments`
