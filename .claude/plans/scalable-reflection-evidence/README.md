# Scalable reflection evidence orchestration

Accumulator: `acc/scalable-reflection-evidence`
Acceptance plan: `docs/acceptance/scalable-reflection-evidence.md`
Epic: none mapped

This DAG follows the actual boundaries of ADR 0298: an engine-only deterministic materializer first; automatic admission and the Build-owned pre-admission lifecycle next; explicit reflection and closed outcomes; coordinator identity; durable proposal verification; client transport/UI projection; then the single API/documentation reconciliation. Workers use offline fakes only. The orchestrator owns acceptance-plan status; protobuf and generated API surfaces have one designated writer each.

| Task | Title | Blocked by | Acceptance criteria |
|---|---|---|---|
| `01-learning-materializer` | Versioned bounded evidence materializer | — | AC1.3, AC2.1, AC2.2, AC3.1, AC3.3, AC3.4, AC3.5, AC6.1, AC6.2, AC6.3 |
| `02-automatic-materialization-admission` | Streaming automatic admission over selected evidence | `01-learning-materializer` | AC1.1, AC1.4, AC3.2, AC5.1, AC5.4, AC7.2, AC8.3 |
| `03-explicit-materialization-lifecycle` | Explicit materialization outcomes and Build lifecycle | `01-learning-materializer` | AC1.2, AC5.2, AC5.3, AC6.4, AC7.1, AC7.3, AC7.4, AC8.1, AC8.2 |
| `04-selected-evidence-coordinator` | Selected-evidence coordinator identity and limits | `02-automatic-materialization-admission`, `03-explicit-materialization-lifecycle` | AC2.3, AC2.4, AC2.5, AC8.4 |
| `05-proposal-manifest-verification` | Durable manifest detail and approval verification | `03-explicit-materialization-lifecycle`, `04-selected-evidence-coordinator` | AC4.1, AC4.2, AC4.3, AC4.4, AC4.5, AC8.5 |
| `06-reflection-client-projection` | Reflection abstention transport and mecatui status | `03-explicit-materialization-lifecycle`, `05-proposal-manifest-verification` | AC9.1, AC9.2, AC9.3, AC9.4 |
| `07-api-and-documentation-reconciliation` | Engine API and selected-evidence documentation reconciliation | `02-automatic-materialization-admission`, `03-explicit-materialization-lifecycle`, `04-selected-evidence-coordinator`, `05-proposal-manifest-verification`, `06-reflection-client-projection` | AC8.6 |

## Serialization notes

Task 01 is the sole owner of the exported `engine/learning` protocol shape; task 07 is the sole writer of `engine/api/*.txt`, `engine/CHANGELOG.md`, and living documentation. Task 05 is the sole owner of proposal-manifest persistence and verification. Task 06 may extend the wire contract after task 03 establishes the closed outcomes, but must not hand-edit generated code.
