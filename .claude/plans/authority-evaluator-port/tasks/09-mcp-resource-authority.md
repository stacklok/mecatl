---
id: 09-mcp-resource-authority
title: Separate MCP resource capability from operation action
blocked_by: [08-vertical-docs]
status: done
branch: "plan-authority-evaluator-port/09-mcp-resource-authority"
worktree: ""
issue: "371"
retries: 0
last_error: "task test baseline macOS /var symlink failures; task docs requires GOPRIVATE"
accumulator: acc/authority-evaluator-port
---

# Repair brief

Protect AC3.8. Add a reserved derived per-server MCP resource capability that
allows resource-only servers without proxying through an unrelated MCP tool.
Add a provider-neutral `Action` to `AuthorityRequest`: capability selection and
operation action are distinct. Require a concrete server for authority-bound
resource operations. Mint/filter resource capabilities from the resolved server
snapshot, preserve Cedar's carried-capability precheck, and ensure Cedar sees
`ListMcpResources`/`ReadMcpResource` as their real action. Delete or replace the
helper-only AC3.8 proof with dispatch-level coverage.

## Acceptance criteria

- AC3.8: A bound run reaches MCP resources through a derived per-server resource capability carried in its set. Resource-only servers are reachable when their own capability is present; aggregate resource operations require a concrete server. The evaluator receives that capability separately from the resource operation action, and no separately authored grant exists.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_ResourceReachDerivesFromToolNames`
