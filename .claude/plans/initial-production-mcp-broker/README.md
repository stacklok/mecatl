# Initial production MCP broker — orchestration index

Accumulator: `acc/initial-production-mcp-broker`  
Integration worktree: current harness-owned-native checkout  
Repair rounds: 0

| Task | Depends on | Scope |
|---|---|---|
| 01-remote-contract | — | Neutral versioned broker protocol and adapter lifecycle |
| 02-remote-failure-semantics | 01-remote-contract | Incarnation, cancellation, deadlines, and no-replay execution |
| 03-broker-auth-callback-service | 01-remote-contract | Authenticated TLS broker service and ToolHive callback boundary |
| 04-mecak8s-continuation | 02-remote-failure-semantics, 03-broker-auth-callback-service | Remote selection and Stage 3 continuation/enrollment |
| 05-production-delivery | 04-mecak8s-continuation | Build, deployment, documentation, topology ADR, and release wiring |
