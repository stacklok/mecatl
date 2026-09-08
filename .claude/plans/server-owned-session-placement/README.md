# Server-owned session placement

Accumulator: `acc/server-owned-session-placement`

This serial graph preserves the completed provider-owned placement foundation, then unifies the existing `session.EnvironmentRef` as the runtime and durable placement identity. The remaining implementation is intentionally one atomic cutover: exact reattachment and path-based public creation cannot coexist in a test-green intermediate. The final documentation-only task records the required re-audit after that cutover.

## Task DAG

```text
01-placement-foundation
  └─ 02-unify-environment-placement-identity
       └─ 03-atomic-server-owned-placement-cutover
            └─ 04-documentation-and-final-audit
```

All 26 acceptance criteria from `docs/acceptance/server-owned-session-placement.md` are assigned exactly once in the task files. Task 02 is an enabling task with no standalone acceptance criterion.
