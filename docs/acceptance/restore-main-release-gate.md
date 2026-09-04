# Restore main release gate — acceptance plan

**Phase:** v0.0.25 release readiness
**Status:** draft, 2026-09-03. Reproduced on `origin/main` at `684df4fae`.
**Accumulator branch:** `acc/restore-main-release-gate` (off `main`).

The smallest set of work restores the required release test gate by making tests use the exact execution-environment identity and canonical workspace root that the running harness uses.

## Why these scope cuts

- [ADR-0291](../adr/0291-server-owned-session-placement.md) makes `EnvironmentRef` the sole runtime identity and requires exact environment reattachment.
- This is a test-fixture correction only: no production behavior, public API, configuration, or user documentation changes.

## In scope — 1 scenario, in implementation order

### Scenario 1 — Release tests exercise exact environment and workspace identities

The role-telemetry integration fixture drives its parent engine with a session and `tool.Environment` carrying the same exact ref. Learning integration fixtures use the canonical workspace root used by the bound osfs workspace when filtering project-partitioned proposals and skills. This preserves the exact-identity placement contract in [ADR-0291](../adr/0291-server-owned-session-placement.md) and the release verification contract in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**

- AC1.1: The role-tagged child metrics fixture completes the parent and Subagent turns without an environment identity mismatch, records one child `Read`, and keeps child token/tool metrics separate from main metrics.
  - verify: `TestChildDriveRecordsRoleTaggedMetrics`, `TestChildMetricsNoDoubleCountAgainstMain`
- AC1.2: Direct SkillDraft and automatic procedure-learning scenarios find their project-partitioned records when the temporary workspace has a physical-path alias such as macOS `/var` → `/private/var`.
  - verify: `TestCloudNativeLearning_Scenario1_DirectSkillDraftRemainsInactive`, `TestUsableAutoSkillsStockBuildPublishesReflectedProcedure`, `TestUsableAutoSkillsStockBuildPolicyMatrix`
- AC1.3: Restarted remote learning finds the proposal and active skill linked to the completed durable attempt under the same canonical project partition.
  - verify: `TestCloudNativeLearning_Scenario3_ExplicitProcedureAttemptSurvivesRestart`
- AC1.4: The complete required release test gate is green.
  - verify: demonstration — `task test` exercises both Go modules with the repository's required race and compatibility gates

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Production placement, learning, or telemetry behavior changes | Not required | Existing behavior already follows [ADR-0291](../adr/0291-server-owned-session-placement.md); only stale test identities are corrected |
| Public API or user documentation changes | Not required | No user-facing behavior changes |

## Sequencing recommendation

Correct the shared role-telemetry fixture first, then canonicalize each affected learning fixture at workspace creation so configuration, execution, and query filters use one identity. Run focused tests before the aggregate gates.

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` passes with the acceptance plan linked from this index.
3. `task api:check` passes without an API baseline change.
4. `task ac-trace-strict` passes after this plan is marked `landed`.
5. `go run ./cmd/mecademo` prints a full offline session.
6. The focused release-blocking tests named above pass on macOS path aliases and ordinary roots.

## Deferred decisions and known risks

- **Platform-specific alias coverage.** Existing macOS temporary-directory behavior supplies the real `/var` alias regression case; no synthetic filesystem abstraction is added.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is satisfied.
