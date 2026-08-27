# mecak8s-kind-fixture plan

Accumulator: `acc/mecak8s-kind-fixture`

The plan separates the operator-run ToolHive-free Kind fixture from the existing
optional vMCP delegation qualification. Tasks progress from the base fixture to
provider wiring, optional Keycloak identity, then the authenticated journey and
documentation.

| Task | Depends on | Acceptance criteria |
|---|---|---|
| `01-kind-base` | — | AC1.1–AC1.4 |
| `02-provider-mode` | `01-kind-base` | AC2.1–AC2.5 |
| `03-keycloak-optional` | `01-kind-base` | AC3.1–AC3.3 |
| `04-auth-validation-docs` | `02-provider-mode`, `03-keycloak-optional` | AC3.4–AC3.7 |
