# Memory lifecycle hardening orchestration

Accumulator: `acc/memory-lifecycle-hardening`.

The six tasks implement the acceptance scenarios in `docs/acceptance/memory-lifecycle-hardening.md`. Dependencies serialize overlapping store, gRPC/proto, and public API changes while leaving the attribution and operator-profile work independently dispatchable early.

| Task | Scope | Depends on |
|---|---|---|
| 01-wire-attribution | Driver boundary scan classification | — |
| 02-undo-semantics | Undo contract and bounded undo ledger | — |
| 03-truncation-visibility | Store/proto/tool truncation signal | 02-undo-semantics |
| 04-profile-allowlist | Operator-profile role allow-list | — |
| 05-driver-wire-hygiene | Uniform lifecycle errors and bounded Inspect | 03-truncation-visibility |
| 06-api-and-tool-hygiene | Remaining classifications, descriptions, API removal, sorting | 02-undo-semantics, 03-truncation-visibility, 04-profile-allowlist, 05-driver-wire-hygiene |
