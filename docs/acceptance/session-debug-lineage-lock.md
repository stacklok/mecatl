# Session debugger root inspection avoids lineage traversal — acceptance plan

**Phase:** capability — session debugger inspection cost and scope boundaries
**Status:** draft.
**Issue:** debugger fix already investigated; accumulator acceptance plan.
**ADR:** [ADR-0256](../adr/0256-session-debugger-evidence-and-reporting.md) — scoped related evidence and revalidation; [ADR-0257](../adr/0257-session-debugger-hardening.md) — bounded JSONL lineage and incarnation revalidation; [ADR-0258](../adr/0258-cryptographic-session-incarnations.md) — incarnation-bound lineage.
**Architecture:** [session debugger](../architecture.md) — the `InspectSession` boundary and root/scoped evidence distinction.
**Accumulator branch:** `acc/session-debug-lineage-lock` (off `main`).

The smallest change that makes root `InspectSession` views cheap and deterministic: `status` and the other basic/root projections must read the authorized target directly and must not traverse the global JSONL lineage index. Omitted scope is canonical; the reserved literal `scope_handle: "root"` is accepted as its compatibility equivalent and normalized before the scan decision and scope resolution. Related/delegation views and reads selected by an opaque descendant scope handle retain their existing lineage scan and root/relationship/ownership/retention revalidation.

## Why this scope cut

- **Root views are not lineage queries.** Snapshot status, transcript, activity, performance, network, and other root projections are target-bound reads; they do not need the global descendant/tombstone inventory. Omitted `scope_handle` is canonical, while the reserved literal `root` is normalized to omission before scan selection and scope resolution. Avoiding that traversal prevents unrelated retained sessions from determining the cost or outcome of a basic inspection.
- **Scoped evidence remains fail-closed.** `related`, `delegation`, and any opaque descendant `scope_handle`-based inspection still use the bounded lineage reader and rescan/revalidate the applicable root, relationship, owner, incarnation, retention, and handle state as required by ADRs 0256–0258. Only handles returned by related evidence select descendants; the debugger prompt tells the model to omit `scope_handle` for root/target views.
- **No contract broadening.** This is an execution-path distinction only. It does not change scope handles, completeness reporting, authorization, retention semantics, or the authoritative snapshot transcript.

## Scenario 1 — Root inspection is lineage-free; scoped inspection is lineage-bound

A debug session inspects a target with a JSONL-backed store containing unrelated retained roots, descendants, and tombstones. An unscoped `InspectSession` call for `status` (and the basic/root view set) returns from the target snapshot/event sources without opening or traversing the global lineage index. The exact reproduced request `{"view":"transcript","scope_handle":"root"}` has the same result: the reserved root token is normalized to omitted scope and performs zero lineage reads. A `related` or `delegation` call, or a call carrying a valid opaque descendant `scope_handle`, performs the bounded lineage scan and the existing incarnation-aware revalidation before projecting evidence. If scoped lineage is unavailable, incomplete, stale, or no longer authorized, the response remains explicit/fail-closed rather than falling back to an unscoped result.

**Acceptance:**

- AC1.1: Unscoped `InspectSession` root views, especially `status`, do not perform global JSONL lineage traversal; their output and completeness fields remain determined by their authoritative root sources.
  - verify: `TestSessionDebuggerInspectSession_Scenario1_RootViewsAvoidLineageTraversal`
- AC1.1a: The exact `view=transcript`, `scope_handle=root` request succeeds as an authorized root transcript and invokes the lineage reader zero times.
  - verify: `TestSessionDebuggerInspectSession_Scenario1_RootTokenTranscriptAvoidsLineageTraversal`
- AC1.1b: The real debug-engine factory places the exact root/descendant scope-handle workflow in the stable system prompt.
  - verify: `TestDebugSessionFactoryExactCatalogAndStablePrefix`
- AC1.2: `related` and `delegation` retain bounded lineage scanning and do not disclose unrelated rows; their existing scan/retention completeness and explicit unavailable/incomplete outcomes remain intact.
  - verify: `TestSessionDebuggerInspectSession_Scenario1_RelatedViewsRetainLineageScan`
- AC1.3: A `scope_handle`-based inspection retains lineage revalidation, including root and child incarnation, relationship, owner, retention, and constant-time handle checks; stale or invalid handles fail closed.
  - verify: `TestSessionDebuggerInspectSession_Scenario1_ScopeHandleRevalidatesLineage`
- AC1.4: The regression proof is deterministic: it uses an instrumented lineage reader or JSONL traversal counter/barrier, asserts zero lineage calls for every unscoped root view, and asserts the scoped paths perform the expected bounded scan without relying on wall-clock timing, goroutine scheduling, or global store ordering.
  - verify: `TestSessionDebuggerInspectSession_Scenario1_DeterministicLineageTraversalBoundary`

## Boundary table

| InspectSession request | Lineage traversal | Required result |
|---|---:|---|
| unscoped `status` | none | authoritative target status; no global lineage dependency |
| root view with reserved `scope_handle: "root"` | none | normalize to omitted scope before scan/resolution; authorized root projection |
| other unscoped root view | none, unless its existing view explicitly requires its own bounded event source | root projection preserves its existing availability/completeness semantics |
| `related` / `delegation` | yes | bounded scan, same-owner descendants only, explicit completeness/retention outcome |
| valid opaque descendant `scope_handle` from related evidence | yes | revalidate root, relationship, incarnation, owner, retention, and handle before projection |
| stale/invalid scoped handle or unavailable lineage | yes, then fail closed | no fallback to an unscoped or guessed target/child |

## Out of scope

| Item | Defer-to / decision |
|---|---|
| Changing lineage record format, JSONL reconciliation, Redis atomicity, or retention policy | [ADR-0257](../adr/0257-session-debugger-hardening.md) |
| Changing scope-handle derivation, incarnation identity, or related-session API | [ADR-0258](../adr/0258-cryptographic-session-incarnations.md) |
| Adding a new debugger projection, changing evidence authority, or changing public protobuf contracts | separate acceptance plan; [ADR-0256](../adr/0256-session-debugger-evidence-and-reporting.md) |
| Removing lineage scans from `related`, `delegation`, or scope-handle reads | explicitly not permitted by this plan |

## Definition of done

1. The deterministic scenario tests prove root views do not invoke global JSONL lineage traversal and scoped views retain the scan/revalidation boundary.
2. `task lint` and `task test` pass (both modules, `-race`).
3. `task docs` passes its generated-reference and strict-link checks.
4. `task ac-trace-strict` resolves every named proof when this plan is landed.
5. No production code or test weakens the ADR-0256–0258 authority, completeness, retention, or incarnation contracts.

## Exit criteria

When the scenario tests and the repository verification commands above pass on the accumulator, this plan is satisfied.
