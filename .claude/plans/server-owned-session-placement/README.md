# Server-owned session placement

Accumulator: `acc/server-owned-session-placement`

This graph first establishes the provider-owned placement protocol, removes physical workspace from durable session state, migrates the public contract, and centralizes exact reattachment. Once that foundation lands, discovery/UI, successors, delegation/artifacts, scheduling, and driver/ACP consumers can proceed in parallel with limited file overlap. The final task owns the cross-cutting path inventory, documentation, and resource-state re-audit.

## Task DAG

```text
01-placement-foundation
  └─ 02-durable-placement-state
       └─ 03-public-placement-contract
            └─ 04-exact-reattachment
                 ├─ 05-session-scoped-discovery
                 ├─ 06-clear-and-fork-successors
                 ├─ 07-delegation-and-artifact-boundary
                 ├─ 08-scheduled-placement
                 └─ 09-driver-and-acp-boundary
                      (all five) ──┐
                                  └─ 10-documentation-and-path-audit
```

All 26 acceptance criteria from `docs/acceptance/server-owned-session-placement.md` are assigned exactly once in the task files.
