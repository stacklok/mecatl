# Authority evaluator port

Accumulator: `acc/authority-evaluator-port`

This plan implements `docs/acceptance/authority-evaluator-port.md` for #371.
Tasks follow the dependency order needed to keep the engine module independent of
Cedar and to make the evaluator enforce one carried, durable capability set.

| Task | Scope | Depends on |
|---|---|---|
| 01-authority-domain | Pure authority value and neutral evaluator port | — |
| 02-session-persistence | Durable session authority payload and restoration | 01-authority-domain |
| 03-execution-evaluator | Local/noop adapters and single execute chokepoint | 01-authority-domain, 02-session-persistence |
| 04-composition-root | Managed definition tier, authority root minting, and evaluator posture | 03-execution-evaluator |
| 05-delegation-derivation | Child derivation, ceilings, per-call tightening, and resume containment | 04-composition-root |
| 06-resource-attribute | Derive a neutral resource descriptor at the execution boundary | 03-execution-evaluator, 05-delegation-derivation |
| 07-cedar-adapter | Opt-in Cedar adapter and static operator policy | 05-delegation-derivation, 06-resource-attribute |
| 08-vertical-docs | Vertical proofs and ADR/living/user documentation | 05-delegation-derivation, 07-cedar-adapter |
| 09-mcp-resource-authority | Repair MCP resource capability/action semantics | 08-vertical-docs |
| 10-authority-disclosure | Filter request disclosure and ToolSearch by authority | 09-mcp-resource-authority |
| 11-vertical-repair | Complete feasible end-to-end authority proof | 09-mcp-resource-authority, 10-authority-disclosure |
