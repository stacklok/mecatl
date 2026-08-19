# ADR 0230 — Redis migration ownership and coverage are proved before mutation

- Status: Accepted
- Date: 2026-08-19
- Scope: Session-migration port ownership, Redis metadata repair, and readiness publication
- Supersedes: ADR 0229
- Superseded by: ADR 0231

## Context

ADR 0229 bound Redis job checkpoints to a renewable fenced acquisition and replaced an
unbounded readiness script with a generation-and-cardinality CAS. Two gaps remained.
Ownership was checked separately before family mutation and readiness publication, so a
lease could be lost between that check and the Lua command. Cardinality was also not a
coverage proof by itself: a missing snapshot row and an orphan index row could offset each
other, while malformed snapshots could keep a job retrying without an honest terminal
result. The base migration-store interface still exposed a lock method that returned no
acquisition context even though Redis mutations required one.

## Decision

1. Make context-carrying acquisition and ownership checking required methods of
   `port.SessionMigrationStore`. Both jsonlstore and redisstore bind their exact acquisition
   to the returned context; mutation and checkpoint calls reject an absent or stale binding.
2. Pass the Redis lock key and exact acquisition token into both metadata-repair and
   readiness Lua scripts. Token comparison is the first script operation, so a stale holder
   performs no writes. Renewal or observed ownership loss also cancels the bound operation
   context, while the script comparison remains the authoritative atomic fence.
3. During a stable-generation inspection, decode every valid snapshot and derive its exact
   metadata member. A v2 snapshot whose hash metadata, global index membership, owner
   metadata, or owner index membership disagrees becomes a bounded repair candidate.
   Repair atomically replaces stale memberships as well as installing missing ones.
4. Permit the constant-work final CAS only after the orchestrator has processed every repair
   candidate and inspection reported no invalid families. It may then compare generation and
   global-index cardinality because per-snapshot coverage was proved during the stable scan;
   an orphan row makes cardinality too large instead of offsetting a missing row.
5. Complete a job containing invalid snapshot families with a nonzero failure count and keep
   Redis metadata paging unavailable. After an operator removes or repairs those snapshots,
   a fresh plan and job can publish readiness. Do not hide invalid families in an opaque
   endless running state.

## Consequences

A process that loses Redis ownership immediately before either mutating script has zero side
effects, and both built-in stores satisfy one usable migration port. Inspection performs
bounded per-snapshot index lookups in the explicit maintenance path; ordinary paging remains
indexed and constant-work per page. Invalid data requires operator repair and a fresh job,
but its state is terminal and truthful. The final Lua operation remains constant-work and
never receives the snapshot key set.

## See also

- [ADR 0229](./0229-redis-migration-fencing.md) — the initial renewable fencing and constant-work publication decision.
- [ADR 0226](./0226-session-storage-maintenance.md) — session storage maintenance workflows.
- [`docs/architecture.md`](../architecture.md) — current Redis deployment behavior.
- [`docs/design/IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md) — migration protocol details.
