# ADR 0100 — Caller identity: accept a principal, thread it everywhere, record the owner

- Status: Accepted
- Date: 2026-08-07
- Scope: the authentication edge, the `Session`/`Schedule` aggregates, the event log, and the internal goroutine principals
- Supersedes: —
- Superseded by: —

## Context

mecatl's authentication edge (`internal/adapter/server/authn.go`) is a single shared
static bearer token compared in constant time. There is no user concept: one
credential, zero subjects. Nothing downstream — the session aggregate, the snapshot,
the event log, the schedule registry, the list row — records *who* is acting, so an
audit question ("who ran this?", "whose schedule fired?") has no durable answer.

The agent-identity model ([`docs/agent-identity-model.md`](../agent-identity-model.md))
names this its phase 1, the "audit trail (unsigned)": before mecatl can enforce
isolation (issue #368), attach sensitivity labels (#369), share deliberately (#370),
or sign anything (Track B), it must first *accept* a verified principal and *thread*
it through every port so the data layer exists and is agreed upon. The original
handover bundled acceptance, the owner, the decision function, the demo, and Redis
transport into one track; scoping it down to accept-and-thread is the cut that makes
it landable without waiting on enforcement design.

Several hazards were verified against the code and shape the decision:

- `authEnabled()` means "a static token is configured" — wrong in both directions (a
  shared-token deployment has zero subjects; an OIDC deployment may have no static
  token). Reusing it as the identity gate is a latent fail-open.
- `sessnap.RestoreState` already carries eight parameters; adding the owner as a
  trailing parameter is a *Changed*/breaking entry under
  [`engine/COMPATIBILITY.md`](../../engine/COMPATIBILITY.md), whereas a
  direct-assignment field (the `Profile`/`ProviderID` pattern) is *Added*/minor.
- The loop is storage-agnostic and must stay so: no adapter/proto/server type may
  cross into `engine/agent` (the layering rule).
- ToolHive's anonymous middleware mints a `sub: "anonymous"` with forged exp/iat — a
  fabricated principal that reaches audit records looking like a real user. That is
  the anti-pattern to avoid.
- `childgc` sweeps an origin session on retention, so a cron schedule that derives
  its owner from the origin session *at fire time* would derive it from a session
  that no longer exists.
- Token validation is hard to get right (alg confusion, key rotation, issuer
  canonicalization, clock skew) and is needed by both mecatl and ToolHive.

## Decision

**Accept a verified principal at the edge, thread it everywhere, record the owner —
and stop.** No enforcement, no per-user keying changes, no signing.

1. **One `Principal` value object.** `{ Issuer, Subject, GrantType, Name? }` in
   `engine/session`. Identity is the `(iss, sub)` pair, never `sub` alone (two IdPs /
   realms collide on `sub`). `GrantType ∈ user | client_credentials | system`. It
   carries no scopes, no authority, no credentials. Modeled on ToolHive's
   `PrincipalInfo`, not imported.

2. **Accept at the existing `authn.go` seam, behind its own predicate.** The OIDC
   verifier extends the current interceptor/middleware; it is enabled by whether the
   verifier is wired, NOT by `authEnabled()`. The verified caller is stashed on an
   unexported context key (`WithPrincipal`/`PrincipalFromContext`). **Absent = nil,
   never fabricated.** Off by default ⇒ byte-identical to today.

3. **Delegate validation to `toolhive-core/authn`.** The shared module both repos
   already depend on owns the JWT/JWKS/discovery logic (`Validate(ctx, bearer) →
   Principal`), hardened on extraction (refresh-on-unknown-kid + negative cache,
   RSA-PSS, byte-exact issuer, leeway + authored nbf/iat). JWT-only; no introspection
   branch. The validator is constructed with the **server-root** context (it owns
   background JWKS refresh). Config: `--oidc-issuer` / `--oidc-jwks-uri` (static,
   short-circuits discovery) / `--oidc-audience` (required when on). The rate limiter
   re-keys off the validated `(iss, sub)`, not the raw token. **OIDC-misconfigured is
   fatal at startup:** with OIDC flags set but validator construction failing (an
   unreachable JWKS and no static `--oidc-jwks-uri` override), the server refuses to
   start rather than silently degrading to the unauthenticated path — the classic
   misconfigured-verifier fail-open. (A mid-flight JWKS outage is different: it
   surfaces as a transient 503-class signal, distinct from a 401 bad token.)

4. **The owner is write-once at `CreateSession`, from the verified token, never the
   request body.** It round-trips the snapshot by **direct assignment** (the
   `Profile` pattern), never a `RestoreState` parameter. Children
   (`subagent-`/`parallel-`/`team-`/`sched--`) inherit the parent's owner — an
   ownerless parent yields an ownerless child, never a fabricated one (this preserves
   the byte-identical no-auth path); a fork inherits the **source's** owner (stamping
   the caller's own owner would make fork an ownership-laundering path); resume keeps
   the persisted owner. **Never backfilled** — a pre-ship session stays ownerless and
   renders unowned. The list row surfaces the owner: proto `SessionSummary` gains an
   `owner` field (a deliberate proto change), the server struct mirrors it, and
   `port.SessionMeta` gains `Owner` so the `MetaLister` fast path (which skips
   `Load`) populates the row identically to the slow path. Display-only — no
   filtering (that is the isolation track's).

5. **The event annotation is log-only, stamped only at `appendEvent`, and names the
   caller who ACTED — not the session's owner.** `session.Event.Actor` is nil at
   every emit site; the relay's `appendEvent` reads the **context principal** (the
   verified caller driving this request) and stamps it. `toProto` omits it (the
   log-only sibling of `EvApproval`/`EvCompactionArchive`); the event-sourced `Fold`
   ignores it (a Fold-rebuilt session keeps the snapshot-restored owner; the fold
   neither requires nor re-derives `Event.Actor`). No proto change on the event path.

   The owner and the actor are **different questions and routinely different
   values**: this phase deliberately ships no authorization, so any authenticated
   caller may act on any session (see Consequences). Deriving `Actor` from the
   session's owner would therefore stamp caller A onto every event of a run that
   caller B drove — repudiation in both directions, and worst on `EvApproval`, where
   the record *is* a human granting a tool permission. The session owner answers
   "whose is this?" and stays the identity of record; `Actor` answers "who did
   this?". A run with no verified caller stamps nil — absence is never fabricated.

   A scheduled fire follows the same rule and therefore names the **scheduler's
   system principal** on its events, while the fire's session still carries the
   schedule owner (decision 6). That is the honest reading: the owner is
   accountable, the scheduler is what acted. Every durable append path stamps
   through the one chokepoint — including the schedule lifecycle events
   (`fired`/`failed`), which must not reach the log unstamped.

6. **A schedule's owner is captured at create time,** never derived at fire time
   (the origin may be swept). The capture rule depends on the create surface: the
   Schedule-tool path reads the *executing session's* owner via the origin binder; an
   out-of-band REST/CLI create (no origin session) reads the *context principal*;
   ownerless/none stays empty (never fabricated). A fire's `sched--` session is
   minted under the scheduler's system-principal context, so an explicit
   owner-injection seam (a `WithOwner` CreateSessionOption sibling of
   `WithSessionID`) overrides the stamp — the fire's session and events carry the
   schedule's owner with `GrantType: client_credentials`, not the system principal.

7. **Internal goroutines run under an explicit system principal, never an absent
   one.** `childgc`, both memory/dream consolidators, and the scheduler
   tick/fire/delivery/reconcile run as `Principal{GrantType: system}`.

8. **Memory, soul, and user-tier skills/commands/rules/agent-defs are deferred.**
   They are ambient per-project / per-OS-user configuration (filesystem stores or
   gRPC drivers wired in `app.Build`, never Redis, never created through the
   authenticated edge), so there is no caller to stamp until they become
   multi-tenant. Their consolidators get the system principal.

## Consequences

**Easier.** The data layer every later track reads now exists and is agreed upon; the
isolation track's decision function reads a complete, durable owner instead of five
separately-unit-tested pieces. The schedule owner survives its origin session. The
audit trail names a real, verified actor. Token validation is shared with ToolHive
and hardened once for both.

**Harder / costs.** The `Event.Actor` field is nil almost everywhere (stamped only at
one site) and denormalizes the acting principal onto every event — deliberate, so an
event read in isolation names who acted. The actor is duplicated per event, and a
reader must hold two identities in mind (the session's owner and the event's actor)
which agree in the single-caller deployment and diverge in the multi-caller one. `Authority` (Track
C) ships inert until that track lands — a dead additive field, accepted to kill a
recurring three-way conflict on generated files. Building against a sibling-developed
`toolhive-core/authn` risks doc/impl divergence; mitigated by a fake-verifier seam.
The cut means **nothing is refused yet** — a deployment gains attribution but no
isolation, which must be stated plainly so it is not oversold.

## See also

- The reasoning model: [`docs/agent-identity-model.md`](../agent-identity-model.md)
  (phase 1, "audit trail (unsigned)").
- The completed [acceptance record](../acceptance/caller-identity.md).
- The layering rule and the persistence/rehydration invariants:
  [`AGENTS.md`](../../AGENTS.md).
- The lifecycle convention: [ADR 0002](./0002-documentation-lifecycle.md); the engine
  stability contract: [ADR 0037](./0037-engine-stability-contract.md); event-sourced
  rehydration: [ADR 0038](./0038-event-sourced-rehydration.md); the resource
  inventory this adds rows to: [ADR 0027](./0027-cloud-native.md).
