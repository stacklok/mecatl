# Server-owned session placement

Accumulator: `acc/server-owned-session-placement`

This serial graph first preserves the completed provider-owned placement foundation, then unifies the existing `session.EnvironmentRef` as the runtime and durable placement identity. It prepares server reattachment and every dependent internal surface before one coordinated public-contract cutover. Transitional `Session.Workspace` remains only until all consumers have migrated, then is removed before the documentation-only final audit.

## Task DAG

```text
01-placement-foundation
  └─ 02-unify-environment-placement-identity
       └─ 03-server-binding-and-exact-reattachment
            └─ 04-session-scoped-discovery-preparation
                 └─ 05-atomic-successor-preparation
                      └─ 06-delegation-and-artifact-boundary
                           └─ 07-scheduled-placement-preparation
                                └─ 08-acp-and-driver-preparation
                                     └─ 09-coordinated-public-contract-cutover
                                          └─ 10-remove-durable-workspace
                                               └─ 11-documentation-and-final-audit
```

All 26 acceptance criteria from `docs/acceptance/server-owned-session-placement.md` are assigned exactly once in the task files. Task 02 is an enabling task with no standalone acceptance criterion.
