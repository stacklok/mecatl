# Session affinity and handoff orchestration

Accumulator: `acc/session-affinity-and-handoff`
Acceptance plan: `docs/acceptance/session-affinity-and-handoff.md`
Epic: none mapped

The DAG keeps the shared header contract ahead of transports, official clients ahead of the end-to-end byte proof, and lease mutation admission ahead of lease-loss, close, drain, and crash-handoff scenarios. Each worker follows strict failing-test-first TDD and uses only offline fakes (`mockllm`, `memfs`, deterministic lease clocks, miniredis, bufconn/httptest).

Shared generated surfaces are serialized: task 07 reconciles engine/TypeScript API reports after their producers, and task 18 is the sole final documentation/configuration-reference writer. The orchestrator alone changes acceptance-plan status. Protobuf source/generated trees must remain unchanged.

| Task | Title | Blocked by | Acceptance criteria |
|---|---|---|---|
| `01-session-header-contract` | Shared session header contract and provider parity | — | AC1.1, AC1.2, AC1.3, AC1.4, AC1.5 |
| `02-grpc-affinity-validation` | gRPC session-affinity validation | `01-session-header-contract` | AC2.1, AC2.2, AC2.3, AC2.4, AC2.5 |
| `03-http-affinity-validation` | HTTP session-route affinity validation | `01-session-header-contract` | AC3.1, AC3.2, AC3.3, AC3.4 |
| `04-mecatui-affinity` | mecatui session-bound metadata propagation | `02-grpc-affinity-validation` | AC4.1, AC4.2 |
| `05-typescript-raw-affinity` | TypeScript raw session-binding helpers | `02-grpc-affinity-validation`, `03-http-affinity-validation` | AC4.4 |
| `06-typescript-high-level-affinity` | TypeScript Session and Run automatic affinity | `05-typescript-raw-affinity` | AC4.3 |
| `07-public-api-reconciliation` | Assembled public API and protobuf reconciliation | `01-session-header-contract`, `04-mecatui-affinity`, `05-typescript-raw-affinity`, `06-typescript-high-level-affinity` | AC4.5 |
| `08-session-mutation-lease-inventory` | Lease-owned session mutation inventory | `01-session-header-contract` | AC5.1, AC5.2 |
| `09-lease-loss-capability` | Lease-loss mutation invalidation and optional compatibility | `08-session-mutation-lease-inventory` | AC5.5, AC5.8, AC5.9 |
| `10-awaiting-lease-loss-handoff` | Awaiting-approval lease-loss settlement | `09-lease-loss-capability` | AC5.6, AC5.7 |
| `11-close-session-semantics` | Lease-aware CloseSession preconditions | `08-session-mutation-lease-inventory`, `10-awaiting-lease-loss-handoff` | AC5.3, AC5.4 |
| `12-graceful-drain-ownership` | Graceful drain ownership ordering | `09-lease-loss-capability`, `10-awaiting-lease-loss-handoff`, `11-close-session-semantics` | AC6.1, AC6.2, AC6.3, AC6.4 |
| `13-modeled-crash-ownership` | Modeled crash stream drop and TTL takeover | `09-lease-loss-capability`, `12-graceful-drain-ownership` | AC7.1, AC7.2, AC7.3 |
| `14-modeled-rehydrate-repair` | Modeled Redis rehydration, repair, and correlation | `01-session-header-contract`, `13-modeled-crash-ownership` | AC7.4, AC7.5 |
| `15-modeled-transport-bytes` | Official-client transport-to-provider byte proof | `03-http-affinity-validation`, `04-mecatui-affinity`, `06-typescript-high-level-affinity`, `14-modeled-rehydrate-repair` | AC7.6 |
| `16-modeled-awaiting-takeover` | Modeled awaiting-approval takeover | `10-awaiting-lease-loss-handoff`, `13-modeled-crash-ownership` | AC7.7 |
| `17-helm-affinity-neutrality` | Helm external-boundary neutrality | — | AC8.1, AC8.2 |
| `18-documentation-reconciliation` | Operator contract, inventories, and generated documentation | `07-public-api-reconciliation`, `10-awaiting-lease-loss-handoff`, `11-close-session-semantics`, `12-graceful-drain-ownership`, `14-modeled-rehydrate-repair`, `15-modeled-transport-bytes`, `16-modeled-awaiting-takeover`, `17-helm-affinity-neutrality` | AC8.3, AC8.4, AC8.5 |

## External gate

AC8.5 is deliberately assigned to the final documentation task because ADR-0291 places its evidence in a separate infrastructure repository and live rollout. The mecatl accumulator can document and preserve that blocking prerequisite but cannot manufacture the required deployed-policy/load-test evidence; final plan landing must treat the external acceptance record as an explicit gate rather than weakening it to an offline chart test.
