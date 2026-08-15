# ADR 0213 — Enforce caller ownership at remote driver boundaries

- Status: Accepted
- Date: 2026-08-11
- Scope: `contracts/proto/mecatl/driver/v1`, `internal/adapter/grpcdriver`, and remote session, memory, schedule, and event-log stores
- Supersedes: —
- Superseded by: —

## Context

[ADR 0212](./0212-caller-ownership-enforcement.md) protects every application-facing
path, but a remote driver exposes raw storage ports beyond that service boundary. It
cannot infer a caller from a session ID, and copying an owner label from a snapshot or
request makes ownership client-asserted. Forwarding a user's bearer token would spread
IdP validation and authorization policy into every driver.

Remote drivers need an enforcement design that preserves the core layering rule:
identity remains a credential-free `session.Principal`; the driver receives identity,
not scopes or authority; and application policy is not rebuilt in the driver.

## Decision

1. **Make B-lite a separate follow-up to ADR 0212.** Until it lands, a driver is
   declared trusted deployment infrastructure. It is never tenant-reachable.

2. **Accept a principal claim only from the authenticated mecatl workload.** The claim
   contains identity fields needed to identify the caller, never an end-user bearer
   token, scope, authority, or delegated permission. It is one protobuf-encoded binary
   gRPC metadata value, parsed once by driver middleware. Production allows only an
   explicit mTLS client workload identity or Unix peer identity; a test-only
   construction may accept a dedicated test CA's peers. A system principal is not a
   valid caller claim for an ordinary caller-owned RPC.

3. **Keep ordinary and infrastructure driver operations separate.** Caller-owned RPCs
   require exactly one valid claim. Global maintenance uses a distinct, explicitly
   classified infrastructure surface authenticated as the workload and cannot invoke
   ordinary caller-owned RPCs. Owner mismatch and absence are `NotFound`; invalid
   claims/peers are authentication or protocol failures.

4. **Use one private durable ownership registry per driver deployment.** The registry
   is injected into session, schedule, memory, and event handlers rather than exposed
   as a public RPC. It records immutable ownership before a payload becomes visible,
   resolves event logs through sessions and fires through schedules, and permits
   same-owner creation retries. Existing records without a registry entry are absent:
   there is no migration and no lazy adoption.

5. **Use a workload-asserted opaque project namespace.** The application resolves and
   canonicalizes its workspace root, derives its SHA-256 digest, and sends only that
   opaque digest with the workload-authenticated caller claim; no raw path crosses the
   driver boundary. The driver keys project memory by `(workspace digest, issuer,
   subject, logical key)` and user-model persistence is not a driver store in this
   B-lite scope. Session and schedule IDs remain opaque, cryptographically random,
   server-minted identifiers keyed with their resource kind.

6. **Keep compatibility explicit.** A driver declares `trusted` or `enforced` mode;
   enforcement is never inferred from metadata presence. Application caller enforcement
   requires configured remote drivers to advertise enforced ownership capability at
   startup. Non-OIDC deployments may deliberately use explicitly trusted drivers.

## Consequences

A direct driver client cannot select another caller's record merely by knowing its ID,
and driver-side enforcement shares ADR 0212's identity comparison semantics without
receiving policy authority. Separate infrastructure operations prevent a scheduler or
retention worker from becoming a universal principal bypass.

The feature adds a driver protocol change, workload-peer configuration, registry
persistence, conformance tests, and a strict cutover: unregistered historical records
become inaccessible in enforced mode. It intentionally does not offer a migration path
or make arbitrary existing driver clients compatible with enforcement.

## See also

- [ADR 0212](./0212-caller-ownership-enforcement.md) — application ownership decision.
- [ADR 0204](./0204-caller-identity-threading.md) — credential-free principal model.
- [Issue #452](https://github.com/stacklok/mecatl/issues/452) — B-lite delivery issue.
- [`AGENTS.md`](../../AGENTS.md) — `engine/` layering and no-fabricated-principal
  invariants.
- [ADR 0005](./0005-driver-seams.md) — driver seams.
