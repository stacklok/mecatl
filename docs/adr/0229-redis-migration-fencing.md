# ADR 0229 — Redis migration uses fenced renewable ownership and indexed coverage

- Status: Accepted
- Date: 2026-08-19
- Scope: Redis session-metadata migration locking, inspection, and readiness publication
- Superseded by: ADR 0230

## Context

A Redis migration drive held a fixed ten-minute `SET NX` lock while its scan and family
work had no matching time bound. The process-local token lookup was keyed only by job ID,
so an expired holder could observe a successor's token and checkpoint or release as that
successor. Final readiness also passed every snapshot key to one Lua invocation, making
publication an unbounded Redis critical section. Redis `SCAN` may duplicate keys, and a
concurrent generation change could label counts from a mixed scan with the newer generation.

The migration must remain caller-driven, restart-safe, generation-bound, and bounded per
family without widening the public engine port solely for Redis mechanics.

## Decision

1. Give every Redis job-lock acquisition a monotonically fenced token plus random nonce.
   Carry that acquisition in the drive context; checkpoint, ownership checks, renewal, and
   release use only that exact acquisition. Never recover a token from a job-ID map.
2. Renew the lock while the drive owns it. A failed renewal, token mismatch, or expiry marks
   ownership lost; the drive performs no further family mutation or checkpoint and a stale
   release cannot delete a successor lock.
3. Retry inspection a bounded number of times until the generation before and after the
   deduplicated scan agrees. Exhaustion returns an explicit `inventory_changed_restart`
   outcome with no counts or candidates from a mixed view.
4. Verify coverage incrementally through the existing bounded per-family CAS installs. The
   stable inspection's unique family count and Redis's duplicate-free global sorted index are
   the coverage proof. Publish `ready` with one constant-work Lua CAS that checks both source
   generation and `ZCARD`; never pass snapshot keys to Lua.

## Consequences

Long migration drives retain ownership without granting stale processes successor authority.
Lock loss is fail-closed and may require the caller to resume. Inspection can ask the operator
to restart under sustained writes instead of returning a mixed plan. Readiness publication no
longer blocks Redis in proportion to store size. Correctness now relies on the existing atomic
Save/Delete/migration scripts maintaining the global metadata sorted index and rebuild
generation together; tests pin that protocol and the constant key/argument count of the final
CAS.

## See also

- [ADR 0226](./0226-session-storage-maintenance.md) — session storage maintenance and bounded jobs.
- [ADR 0027](./0027-cloud-native.md) — Redis resources and restart fidelity.
- [`docs/architecture.md`](../architecture.md) — current Redis deployment behavior.
- [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) — migration protocol details.
