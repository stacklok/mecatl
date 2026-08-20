---
id: 10-authority-disclosure
title: Filter disclosed tools and ToolSearch by authority
blocked_by: [09-mcp-resource-authority]
status: done
branch: "plan-authority-evaluator-port/10-authority-disclosure"
worktree: ""
issue: "371"
retries: 0
last_error: "task test baseline macOS /var symlink failures"
accumulator: acc/authority-evaluator-port
---

# Repair brief

Protect AC3.6 and clarify AC3.7. Filter `LLMRequest.Tools` and ToolSearch
results to carried authority while retaining `execute` as the independent
enforcement boundary. Preserve required control/run-scoped tools. Disclose
`CallMcpWithQuery` only when at least one remote target is reachable; keep its
target-only authorization semantics and pin both allowed/denied target cases.

## Acceptance criteria

- AC3.6: Capability filtering at disclosure and at ToolSearch shapes the request only and is never relied on for enforcement. Dispatch refuses independently: a tool that is disclosed but absent from the derived set is still refused at `execute`, and no enforcement site survives between lookup and dispatch.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_DisclosureIsNotLoadBearing`
- AC3.7: A call to `CallMcpWithQuery` is authorized against the remote tool it targets, not against the meta-tool's own name: the decorator reconstructs `mcp__<server>__<tool>` from the call arguments and applies the same predicate as `execute`, refusing with a message naming the reconstructed target. The meta-tool is a transport helper, not a second grant, and is disclosed only when a reachable target exists.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_MetaToolIsAuthorizedAgainstItsTarget`
