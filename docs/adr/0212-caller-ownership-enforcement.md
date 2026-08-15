# ADR 0212 — Enforce caller ownership at every application access path

- Status: Accepted
- Date: 2026-08-11
- Scope: application-facing sessions, schedules, teams, memory, event streams, live runs, and model-facing object access
- Supersedes: —
- Superseded by: —

## Context

[ADR 0204](./0204-caller-identity-threading.md) introduced a verified `session.Principal`
and durable session and schedule owners, deliberately without authorization. That makes
attribution possible but leaves every application path able to operate on an ID supplied
by any authenticated caller. The exposed paths are not limited to durable stores: an
in-flight run can be approved, cancelled, or have its mode changed without a store
round trip, and model-facing delegation/memory tools can reach shared ports directly.

The driver protocol remains explicitly trusted infrastructure in this phase. It is an
out-of-process raw-port boundary and cannot enforce the application decision until the
separate B-lite work in [ADR 0213](./0213-driver-caller-ownership.md) lands. Its trust
requirement must therefore be explicit rather than implied by the application's checks.

## Decision

1. **Treat `(Issuer, Subject)` as the owner identity.** A caller owns a resource only
   when both fields from the verifier equal its durable owner. `Subject` alone, a
   caller-supplied owner, display name, grant type, or independently-normalized issuer
   is never enough. A verified caller may not access an ownerless pre-identity resource;
   it is not adopted. This enforcement activates when an OIDC verifier is configured.
   With no verifier, the existing ownerless deployment remains byte-compatible and does
   not manufacture a caller. Creation binds that identity atomically with making a
   resource visible: a same-owner retry is idempotent only for the same immutable create
   request, while a cross-owner ID collision is absence-style and can never overwrite.

2. **Use one ownership decision function and one per-kind table.** The table classifies
   sessions, schedules, teams, user-model memory, project memory, and derived resources.
   Event logs and live runs resolve through their owning session; schedule fires resolve
   through their schedule; forks and carryover resolve their source session before any
   history is copied. User-model memory (`RememberUser`/`RecallUser`/`SearchUserModel`)
   and project memory (`Remember`/`Recall`/`SearchMemory`/`Forget`) are separate kinds:
   their local backing stores and tool families remain distinct even when their logical
   keys match. A source-level guard inventories the designated application-facade,
   in-memory-registry, event-relay, cache/index, and model-tool access boundaries and
   fails when a call has no table entry. The table therefore classifies every access as
   caller-owned, derived, shared infrastructure, or an explicit exemption.

3. **Enforce at every application object boundary, on every request.** Store decorators,
   service run-entry/live-run methods, event streaming, in-memory run/lease maps, and
   model-facing tools all call the same decision. A passed initial run-entry check is not
   a grant for later calls. Lists are owner-filtered before pagination, counts, cursors,
   or page boundaries are calculated; no list metadata may reveal a non-owned object.

4. **Deny as absence.** A missing resource, an owner mismatch, and a missing/non-owned
   parent return the same not-found result to the caller. Authorization occurs before a
   foreign request can signal a run, acquire a run-entry lock, persist, append an event,
   or emit a target-correlated diagnostic. No response exposes an owner, resource
   existence, or policy explanation. Authentication failure remains distinct from
   resource lookup failure.

5. **Use explicit system principals only for classified shared infrastructure.** Internal
   workers do not receive a universal ownership bypass. Their allowed operations are
   named in the same kind table; all other system access is denied. The model-facing
   posture ladder is orthogonal and cannot turn ownership enforcement off.

6. **Declare and verify the driver trust boundary until ADR 0213.** A raw driver endpoint
   is deployment-internal only: a supported OIDC deployment must select and prove one
   concrete boundary (NetworkPolicy, mTLS pinning, or a Unix socket) that admits the
   mecatl workload and denies a tenant peer. It is not tenant-reachable and must not be
   represented as application-enforced.

## Consequences

Application callers receive a single, consistent isolation rule whether they name a
persisted session, a live run, a transcript, or a model-reachable child identifier.
Pre-identity records are safely unavailable in OIDC deployments instead of being claimed
by the first caller. Existing non-OIDC deployments retain their current compatibility
path.

This adds classification work whenever an object-touching method is introduced and
requires negative tests for non-store access paths. It does not secure a directly
reachable raw driver endpoint; operators must preserve the stated deployment boundary
until ADR 0213 is implemented.

## See also

- [ADR 0204](./0204-caller-identity-threading.md) — principal and durable owner
  attribution.
- [ADR 0213](./0213-driver-caller-ownership.md) — the deferred driver enforcement
  boundary.
- [Issue #368](https://github.com/stacklok/mecatl/issues/368) — application caller
  isolation.
- [`AGENTS.md`](../../AGENTS.md) — no fabricated principal and layering invariants.
- [ADR 0002](./0002-documentation-lifecycle.md) — documentation lifecycle.
