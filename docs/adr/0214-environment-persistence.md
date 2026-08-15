# ADR 0214 — Execution-environment persistence and reattachment

- Status: Accepted
- Date: 2026-08-13
- Scope: `engine/session` (`Session.EnvironmentRef`, snapshot persistence); `engine/adapter/sessnap` (`Snapshot.EnvironmentRef`); `internal/adapter/server` (`Config.EnvironmentResolver`, run-entry reattachment, create-time ref stamping); `internal/adapter/remoteenv` (the in-process reference fake); `engine/COMPATIBILITY.md` (Session reconstruction contract)
- Supersedes: [ADR 0211](./0211-execution-environment-runtime-seam.md) — ONLY for its phase-3 deferral (decision 6, which deliberately left `EnvironmentRef` an in-process identity and reattachment unimplemented). ADR 0211's runtime seam (the `Environment`/`EnvironmentForker`/`EnvironmentMerger`/`Tool.Execute`/`CommandRunner` signatures, decisions 1–5) remains authoritative and is NOT superseded. [ADR 0208](./0208-execution-environment.md)'s version-aware file mutation is likewise untouched.
- Superseded by: none

## Context

ADR 0211 (the runtime seam) made `session.EnvironmentRef` an in-process identity: it carried the
backend-family `Kind` + opaque `ID` on a live `tool.Environment`, but it was deliberately NOT a
snapshot field. A process restart re-derived the `Environment` through the existing per-session
engine rehydration path (which rebuilds the engine for a persisted provider/model selector and,
for no-fs, re-registers the override); the ref was not yet a reattachment input. That deferral was
honest for the runtime-seam PR, which had to land the bound-runner migration first.

The deferred work is the persistence/reattachment half: make the ref durable so a restarted process
can reattach a live `Environment` to the SAME backend (a remote worker, a container) instead of
silently re-deriving one from the workspace/profile, and prove the reattachment/fork/merge contract
over a non-in-tree `EnvironmentKind` without waiting for a real vendor transport. The risk is the
opposite of ADR 0211's: an EnvironmentResolver wired at run entry could silently trigger per-session
engine rehydration for a default provider/model, conflating two independent concerns
(engine rehydration rebuilds the ENGINE for a persisted selector; environment reattachment
reattaches the ENVIRONMENT for a persisted ref). A ref that names a backend the server cannot
resolve must fail loudly, never silently fall back to a local workspace (that would be the same
silent-degradation class ADR 0211 closed for the no-fs override).

## Decision

### 1. Persist `Session.EnvironmentRef` as an inert exported field

`session.Session.EnvironmentRef` (added in ADR 0211) is now a DURABLE, inert exported field, the
same write-once-label posture as `Profile`/`ProviderID`/`ModelID`/`ReasoningEffort`: the aggregate
stores it and never interprets it. `sessnap.Snapshot.EnvironmentRef` round-trips it through
`Of`/`Restore`. It uses Go 1.26's `omitzero` (NOT `omitempty`, which never omits a non-empty struct)
so a default/local session with no remote ref stays byte-identical to a pre-phase-3 snapshot —
purely additive, no format-tag bump. A legacy snapshot with no `environment_ref` key restores the
zero ref and remains backward-compatible. `session.Session.Workspace` is NOT removed.

### 2. Stamp the resolved default ref at create time; lazily stamp legacy sessions

At `createSession` the composition root stamps the resolved default `EnvironmentRef` after the
session is built: `local` (ID = workspace root) for a filesystem session, `nofs` (empty ID) for a
no-fs / empty-workspace session. A non-zero ref is never overwritten (a re-created carryover fork
inherits its labels; an override creator stamps its own). Legacy sessions with a zero ref are
stamped from the FIRST successfully resolved live `Environment` at run entry so the next ordinary
save persists it — no migration sweep.

### 3. Add a minimal `server.Config.EnvironmentResolver` seam

`Config.EnvironmentResolver func(context.Context, session.EnvironmentRef) (tool.Environment, error)`
is the reattachment half. Context is required (a future network backend may dial out).
`Environment` overrides remain the highest priority. A zero ref uses the legacy
Workspace-derived resolution; the in-tree Kinds (`local`/`mem`/`nofs`) resolve through the existing
`Workspaces`/`CommandRunnerFactory` path and NEVER reach the resolver. Any OTHER `Kind` requires
the resolver; a nil resolver, a ref mismatch, or a nil-`Workspace` result fails loudly with
`ErrFailedPrecondition` — never a silent local fallback. The returned `Environment.Ref()` MUST
equal the requested ref. The resolver does NOT trigger or rebuild a per-session `SessionEngine` for
a default provider/model: environment reattachment and engine rehydration are INDEPENDENT (a session
may need either, both, or neither). The resolver is composition-owned; the loop and the engine stay
storage/transport-agnostic.

### 4. `internal/adapter/remoteenv` is a deterministic reference fake, not a default wiring

`internal/adapter/remoteenv` is an in-process, protocol-level reference fake of a remote
environment: no network, no external dependency, no global singleton. It owns a `Backend`
with an ID→namespace registry; `NewEnvironment(id)`/`Resolve(ctx, ref)` return a `Workspace` +
`CommandRunner` bound to the same opaque namespace; `EnvironmentForker` returns a complete isolated
child `Environment` with a fresh opaque child ID and cleanup; `EnvironmentMerger` applies child
changes to the parent by REF, preserving the child on conflict. The fake runner implements a tiny
documented test protocol (`cat <path>`, `write <path> <content>`); it does not hand-roll a general
shell. `FileVersion` tokens serialize via `Token`/`NewFileVersion`; conditional CAS and create-only
hold across multiple handles to the same namespace. Its `EnvironmentKind` label is
`const Kind session.EnvironmentKind = "remote-fake"` inside the adapter — NOT a constant in the
public `engine/session` (the `EnvironmentKind` set is open; a future remote transport adds its own
label without widening that package). The fake is a contract proof only and is NOT wired by default
`app.Build`.

### 5. Remote transport stays deferred

This ADR does NOT choose RPCs, leases, upload protocols, path syntax, credential forwarding,
backend CAS tokens, or cleanup ownership for a remote execution service. The ref is durable
identity, not a transport contract. A remote service must provide true backend compare-and-swap and
explicit lifecycle semantics when designed; local adapters continue to make only the narrower
same-live-`Workspace` guarantee ADR 0208 states. No stdio MCP or command-spawning transport is
implied.

## Consequences

**Benefits:**

- A restarted process reattaches a live `Environment` to the same backend for a non-in-tree `Kind`
  instead of silently re-deriving one — the no-fs/silent-degradation class is closed for remote envs
  too.
- The phase-3 fork/merge/reattachment contract is proven by a deterministic fake, so a real vendor
  transport has a reference to conform to without a multi-month blocking dependency.
- Environment reattachment and engine rehydration stay independent: a remote session on a default
  provider/model rides the shared engine and never pays a per-session rebuild just because its
  `Environment` is remote.
- Legacy snapshots and the in-tree backends are byte-identical: the field is additive, the resolver
  is nil by default, and the in-tree Kinds never reach it.

**Costs and limits:**

- `sessnap.Snapshot` gains a struct field that uses `omitzero` (the first snapshot field to do so;
  the prior struct fields use pointers for true omitempty, which `EnvironmentRef`'s value-type
  design rejects — ADR 0211 made it a plain comparable struct to avoid heap escapes on the hot
  dispatch path). `omitzero` requires Go 1.26, which the module already targets.
- The fake is contract proof only: it does not exercise a real network, real CAS, or real cleanup,
  and a vendor transport will need its own conformance against the same contract.
- Reattaching per-run (the reattached `Environment` is not cached as an override) mirrors the
  default path's fresh-per-run construction; a future transport that wants a durable session-scoped
  reattached `Environment` will need to register it as an override explicitly.
- Remote transport itself remains work; this ADR closes the persistence/reattachment/reference-fake
  phase only.

## See also

- [ADR 0208 — execution environments and version-aware file mutation](./0208-execution-environment.md)
- [ADR 0211 — execution-environment runtime seam](./0211-execution-environment-runtime-seam.md)
- [Architecture — ports and adapter boundaries](../architecture.md)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md)
- [ADR 0027 — cloud-native state and resource inventory](./0027-cloud-native.md)
- [ADR 0036 — engine module boundary](./0036-engine-module.md)
