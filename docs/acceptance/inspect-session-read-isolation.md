# InspectSession lineage reads are isolated — acceptance plan

**Phase:** capability — stored-session debugger lineage isolation
**Status:** in-progress.
**Issue:** scoped `InspectSession` follow-up; not landed.
**ADR:** [ADR 0320](../adr/0320-inspect-session-lineage-read-isolation.md) *(proposed)*; preserves [ADR 0256](../adr/0256-session-debugger-evidence-and-reporting.md), [ADR 0257](../adr/0257-session-debugger-hardening.md), and [ADR 0258](../adr/0258-cryptographic-session-incarnations.md).
**Architecture:** [session debugger](../architecture.md) — current debugger behavior; this plan does not describe it as shipped.
**Accumulator branch:** `acc/inspect-session-read-isolation` (off `main`).

This follow-up specifies pure, physically bounded debugger lineage lookups. Root `related` and `delegation` enumerate direct edges only. A returned v2 opaque self-routing handle selects a deeper descendant; the server proves its bounded backward ancestry to the authorized root before projection. Reads never repair or serialize unrelated sessions; ordinary writes update only their target record and incident edges, while targeted crash recovery fails closed on uncertain state.

## Scenario 1 — Isolated direct inspection and verified deeper navigation

An analyst opens a debug session bound to a retained root while unrelated roots are being created, deleted, or recovered. Root `related` and `delegation` return only the root's direct, same-owner, incarnation-matching edges from its bounded edge partition; neither call scans, locks, reconciles, nor waits on unrelated lineage. A direct child yields a v2 opaque handle. Using it for a deeper view resolves its sealed routing claim and verifies at most the configured backward ancestry depth to the root, including every edge, owner posture, retention, and incarnation. A missing, stale, tampered, legacy, or crash-uncertain edge fails closed with an explicit incomplete/unavailable result. Ordinary create/update/delete operations touch only the subject row and its affected direct edges; recovery is targeted and never makes a read repair or guess global lineage.

**Acceptance:**

- AC1.1: Root `related` and `delegation` enumerate direct edges only through a bounded, target-partitioned read and do not scan, lock, reconcile, or block on unrelated sessions.
  - verify: `TestInspectSessionLineageIsolation_Scenario1_RootReadsArePureAndPartitioned`
- AC1.2: A v2 opaque self-routing handle reaches a descendant only after bounded backward ancestry proof of exact root/edge/incarnation/owner/retention state; bad or stale handles fail closed without a forward scan.
  - verify: `TestInspectSessionLineageIsolation_Scenario1_HandleBackwardProof`
- AC1.3: Targeted ordinary writes maintain the changed record and direct edges atomically where the backend supports it; targeted crash recovery exposes uncertainty rather than repairing during a read or admitting a guessed relationship.
  - verify: `TestInspectSessionLineageIsolation_Scenario1_TargetedWriteAndCrashRecovery`
- AC1.4: The v1 descendant-handle format is never revalidated by a global scan: it returns a refresh-required failure, while omitted scope and the reserved `scope_handle: "root"` retain their existing root behavior and a fresh `related` response emits v2 handles.
  - verify: `TestInspectSessionLineageIsolation_Scenario1_LegacyHandleMigration`

## Boundaries and migration

| Surface | Contract in this plan |
|---|---|
| Root `related` / `delegation` | Direct edges only; bounded partition read, not recursive enumeration. |
| Deeper descendant | v2 handle only; sealed self-routing claim plus bounded backward ancestry proof. |
| Root scope | Omitted scope and literal `root` remain root selection, never descendant handles. |
| v1 descendant handle | Reject fail-closed with refresh guidance; never restore global scan compatibility. |
| Existing lineage rows | Migrate by explicit/maintenance or ordinary targeted write; absent/unsettled targeted index is incomplete/unavailable, not a read-time repair. |
| Authority and evidence | Preserve ADRs 0256–0258: target binding, owner posture, retention/completeness disclosure, cryptographic incarnations, typed-edge validation, and no raw IDs. |

## Out of scope

- New evidence projections, public protobuf fields, raw-ID selectors, or changes to the debugger reporting approval boundary.
- Relaxing ADR 0256–0258 authorization, retention, completeness, tombstone, or incarnation rules.
- A global read-time migration, global lineage lock, or a best-effort fallback from v2/v1 handles.

## Definition of done

1. The scenario proofs demonstrate direct-edge root reads, bounded backward proof, targeted write maintenance/recovery, and old-handle failure behavior deterministically.
2. `task lint`, `task test`, and `task docs` pass.
3. This plan remains `in-progress` until the accumulator contains the implementation and proofs.
