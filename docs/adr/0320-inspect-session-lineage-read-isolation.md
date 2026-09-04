# ADR 0320 — Isolated direct-edge lineage reads for `InspectSession`

- Status: Proposed
- Date: 2026-09-04
- Scope: stored-session debugger lineage storage, traversal, and scope-handle migration
- Supersedes: —
- Superseded by: —

## Context

ADRs 0256–0258 make `InspectSession` target-bound, fail closed, incarnation-aware, and
explicit about retention and completeness. The current bounded lineage reader still discovers
related sessions by scanning a shared lineage population. Even with a record limit, an
unrelated retained root can determine a target read's I/O, lock contention, and failure mode.
A read path that reconciles interrupted state also turns an analyst request into a mutation and
can globally serialize otherwise unrelated session work.

The debugger needs two different operations. At the root, an analyst needs the immediate
children and direct delegation facts. After selecting one, the analyst may need a deeper
descendant. The first operation does not need recursive discovery; the second must prove that
the selected descendant belongs to the exact target incarnation without trusting a client- or
model-supplied identifier. Neither operation justifies a global scan or a read-time repair.

## Decision

Make debugger lineage reads pure and physically bounded.

Store and read direct lineage edges in a partition addressed by the exact subject
`(session ID, incarnation)`. Root `related` and `delegation` enumerate only that root's direct
same-owner, retention-valid, typed and incarnation-matching edges. They neither recurse nor
scan a shared lineage population. Per-partition limits and deterministic ordering bound the
response; an unavailable, corrupt, or incomplete partition is reported as such and does not
fall back to a global scan.

Issue v2 opaque, versioned, self-routing descendant handles from retained direct-edge evidence.
The deployment seals and authenticates the routing claim so it does not disclose raw IDs or
incarnations. Its version and claim bind the target fingerprint, selected descendant identity
and incarnation, and enough ancestry information to route the request. A deeper request opens
only that claim and proves the chain backward from the selected descendant to the authorized
root, bounded by the configured maximum depth. At every hop it verifies the exact typed edge,
incarnations, owner posture, retention, and target binding. A malformed, tampered, stale,
foreign, missing, or over-depth claim fails closed; it never causes forward enumeration or a
best-effort relationship inference.

Keep ordinary mutations targeted. Create, update, and delete maintain the subject record and
its affected direct-edge partitions atomically when the backend offers an atomic primitive;
otherwise they use the backend's existing targeted durable protocol. Recovery operates only on
the named record/edge operation. It must never run as a debugger read, acquire a global lineage
lock, or infer a completed edge from partial state. If a targeted crash state cannot be proved,
the read reports unavailable or incomplete and denies descendant navigation.

Migrate without restoring scan-shaped reads:

- Existing root selection remains unchanged: omitted `scope_handle` and the reserved literal
  `root` select the authorized root.
- Existing v1 descendant handles are recognized only to return a fail-closed
  refresh-required result. They are never revalidated by a legacy global scan.
- Fresh root `related` evidence issues only v2 handles. Existing debug sessions need no handle
  snapshot migration because handles remain request values, not durable authority.
- A migration/maintenance pass and ordinary targeted writes may materialize direct-edge
  partitions for existing records. Until the required target partition is established and
  trustworthy, root evidence is explicitly incomplete or unavailable and deeper navigation is
  refused. No read performs backfill or reconciliation.

This ADR preserves ADRs 0256–0258. In particular, target binding, owner-posture checks,
retention/completeness disclosure, typed-edge validation, cryptographic incarnations, tombstone
semantics, constant-time handle comparison where applicable, and the prohibition on raw-ID
scope selectors remain required. It changes only how qualifying lineage evidence is found and
how a deeper selected descendant is revalidated.

## Consequences

Unrelated lineage cannot make a root debugger request scan, block, or mutate global state, and
an explicit descendant navigation has a bounded proof cost proportional to its ancestry rather
than to retained session population. The direct-edge partition becomes a durable index that
ordinary writes and targeted recovery must maintain, including backend-specific atomicity and
crash tests.

Older descendant handles deliberately stop working after the v2 cutover; users/models refresh
`related` evidence to continue. Retained legacy data can temporarily produce explicit
incomplete/unavailable debugger evidence until migrated, rather than the more convenient but
unsafe global fallback. This is a proposed decision: no current behavior is claimed to have
changed until the implementation and acceptance plan land.

## See also

- [ADR 0256](./0256-session-debugger-evidence-and-reporting.md)
- [ADR 0257](./0257-session-debugger-hardening.md)
- [ADR 0258](./0258-cryptographic-session-incarnations.md)
- [Acceptance plan](../acceptance/session-debug-lineage-lock.md)
- [Architecture overview](../architecture.md)
- [Production readiness tracker](../design/PRODUCTION-READINESS.md)
