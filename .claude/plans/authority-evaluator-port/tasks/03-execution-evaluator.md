---
id: 03-execution-evaluator
title: Enforce authority once at the execution chokepoint
blocked_by: [01-authority-domain, 02-session-persistence]
status: done
branch: "plan-authority-evaluator-port/03-execution-evaluator"
worktree: ""
issue: "371"
retries: 0
last_error: "task test baseline blockers: macOS /var symlink checks and skills lifecycle workspace test; task docs cannot fetch matlatl"
accumulator: acc/authority-evaluator-port
---

# Task brief

Wire the neutral evaluator into `Engine.execute`, exactly once on every tool
execution path. Implement clear model-visible denial versus evaluator-unavailable
errors and operator diagnostics for unavailable evaluators. Keep catalog and
ToolSearch filtering as request shaping only and prove it is not enforcement.
Add the `CallMcpWithQuery` target-name decorator at the dispatch boundary and
derive MCP resource reach from carried names. Do not add composition flags,
child derivation, or Cedar.

## Acceptance criteria

- AC3.1: Every tool execution passes the evaluator exactly once, from every dispatch path: sequential, read-parallel batch, awaiting-approval resume, cross-process resume, and guardrail approve-once.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_EveryDispatchPathConsultsTheEvaluatorOnce`
- AC3.3: A denial and an evaluator failure are distinguishable at the call site and produce different model-visible messages; an evaluator failure fails closed and emits an operator diagnostic.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_UnavailableEvaluatorIsDistinctFromDenial`
- AC3.5: The three adapters — noop, local, and Cedar — satisfy one shared conformance suite, including identical fail-closed behaviour on a malformed request.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_AdaptersSatisfyConformanceSuite`
- AC3.6: Capability filtering at disclosure and at ToolSearch shapes the request only and is never relied on for enforcement. Dispatch refuses independently: a tool that is disclosed but absent from the derived set is still refused at `execute`, and no enforcement site survives between lookup and dispatch.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_DisclosureIsNotLoadBearing`
- AC3.7: A call to the `CallMcpWithQuery` meta-tool is authorized against the remote tool it targets, not against the meta-tool's own name: the decorator reconstructs `mcp__<server>__<tool>` from the call arguments and applies the same predicate as `execute`, refusing with a message naming the reconstructed target.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_MetaToolIsAuthorizedAgainstItsTarget`
- AC3.8: A bound run may reach an MCP server's resources only if its derived set contains at least one tool name from that server; the reach is derived from the carried names and is not a separately authored grant.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_ResourceReachDerivesFromToolNames`
