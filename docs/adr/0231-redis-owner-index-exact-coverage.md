# ADR 0231 — Redis readiness requires exact owner-index coverage

- Status: Accepted
- Date: 2026-08-19
- Scope: Redis derivative metadata verification and readiness publication
- Supersedes: ADR 0230

## Context

ADR 0230 proved that every valid snapshot had its expected global and owner membership, then
used global-index cardinality in the final publication CAS. That did not prove the converse:
an owner sorted set could also contain an orphan, malformed member, or a valid row belonging
to another owner. The global cardinality could remain exactly equal to the snapshot count,
allowing readiness even though owner paging would expose a ghost row or fail on corruption.
Removing such entries during migration inspection would violate the read-only planning
contract, while passing every member to one Lua script would make publication O(total).

## Decision

1. Before readiness publication, derive the exact global and per-owner member sets from all
   authoritative snapshots using bounded client-side `SCAN` and hash reads.
2. Compare those sets in both directions against the global and every extant owner sorted set
   using bounded `SCAN`/`ZSCAN` batches. Missing, orphaned, malformed, and wrong-owner
   memberships all fail the proof. Inspection remains read-only and never removes a mismatch;
   unmatched memberships are reported as a coverage failure for explicit operator repair.
3. Read the rebuild generation before and after verification. After a successful proof, keep
   the final Lua operation constant-work and have it atomically recheck the exact generation,
   migration lock token, and global cardinality before publishing readiness. Every normal
   Save/Delete changes indexes and rebuild generation in one Redis script, so concurrent
   mutation cannot invalidate the proof and still pass publication.

## Consequences

The explicit maintenance finalization path performs O(total snapshots plus index members)
client work, in bounded Redis commands, while ordinary owner paging and the publication Lua
script remain bounded. Corrupt derivative owner memberships now keep paging unavailable and
require deliberate operator repair instead of being silently deleted during planning. A
concurrent Save/Delete may force a harmless migration restart, but can never publish a stale
proof.

## See also

- [ADR 0230](./0230-redis-migration-atomic-ownership-and-coverage.md) — the superseded per-snapshot coverage decision.
- [`docs/architecture.md`](../architecture.md) — current Redis migration behavior.
- [`docs/design/IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md) — migration protocol details.
