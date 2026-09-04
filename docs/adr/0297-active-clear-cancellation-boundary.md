# ADR 0297 — Active clear cancellation boundary

- Status: Proposed
- Date: 2026-09-04
- Scope: `ClearSession` semantics when the source session is running or awaiting approval
- Supersedes: [ADR 0291](./0291-server-owned-session-placement.md) only for active-source `ClearSession` failure semantics
- Superseded by: none

## Context

ADR 0291 defines `ClearSession` as a non-destructive successor operation whose failure
changes neither the source nor the client binding. That is achievable while the source
is inactive and for failures rejected during preflight. It is not universally achievable
when clear is accepted while a source is running or awaiting approval.

An active clear must stop the exact running lifecycle or consume the durable pending
approval before a replacement can safely become the client's prompt target. After that
cancellation, successor creation still has several independent failure points: acquiring
or retaining the mutation lease, preparing or exactly reattaching placement, constructing
a per-session engine, and durably persisting the successor. The cancellation cannot be
rolled back honestly if any later operation fails. A running turn may already have emitted
output or mutated its workspace, and an awaiting approval has already been resolved by
cancellation.

Promising that every failure leaves the source unchanged would therefore either be false
or require a distributed transaction spanning an in-process run, lease backend, placement
provider, engine resources, and session store. Those systems do not share a transaction
protocol.

## Decision

Treat `ClearSession` submitted against a running or awaiting source as an explicit
**abandon-and-replace** operation.

Validate every condition that can be checked without changing the source before
cancellation. In particular, reject an invalid, stale, foreign, or unavailable explicit
worktree selector while an awaiting source and its pending approval remain unchanged.

Once active-source cancellation begins, cancellation is the irreversible semantic
boundary. Under the service mutex, clear first marks the exact registered lifecycle as
cancelling and only then signals cancellation. Approval delivery, steer admission or
promotion, and provisional run promotion refuse that marked lifecycle; deregistration
removes the marker with that exact lifecycle, so it cannot affect a later run using the
same session ID. Clear waits for the marked lifecycle to settle and durably cancels an
awaiting source before publishing a successor. If a later lease, placement, engine
construction, or persistence step fails:

- publish no successor and perform no client rebinding;
- allow the original source to remain terminal-cancelled;
- keep another clear attempt valid against that source; and
- never roll back workspace or tool mutations made before cancellation.

A successful retry creates the same distinct empty-history successor required by ADR
0291. The source remains stored; clear does not delete or rewrite its history into the
successor.

Do not claim atomicity across cancellation, lease acquisition or renewal, placement
preparation, engine construction, and successor persistence. The operation guarantees
preflight non-mutation where possible and atomic successor publication at the store
boundary, not distributed rollback of an already-cancelled source.

This decision supersedes ADR 0291 only where ADR 0291 says an active-source clear failure
cannot change the source. ADR 0291 remains authoritative for server-owned placement,
selector scope, exact reattachment, path-free public contracts, and idle-source successor
semantics.

## Consequences

- Users can clear at any point, including during streaming and approval, without a false
  all-or-nothing guarantee.
- A failed active clear keeps the TUI bound to the source and publishes no replacement,
  but that source may now be cancelled rather than still running or awaiting. While the
  source stream generation has not yet delivered its delayed terminal result or close,
  the TUI keeps source input and approval blocked; after settlement it returns to an idle,
  retryable source display instead of running normal continuation logic.
- Retrying clear is safe and is the recovery action after the local lifecycle settles.
- Failures rejected before cancellation retain the stronger behavior: no source state
  change, no successor, and no client rebind.
- Operators must treat pre-cancellation workspace mutations as durable external effects;
  clear is conversation replacement, not filesystem rollback.
- Full all-or-nothing semantics would require a coordinated transaction protocol across
  heterogeneous runtime and storage systems, which this architecture deliberately does
  not claim.

## See also

- [ADR 0291 — Server-owned session placement](./0291-server-owned-session-placement.md)
- [Architecture guide](../architecture.md)
- [Usage and operator guide](../usage.md)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md)
