# ADR 0105 — Execution-environment runtime seam

- Status: Accepted
- Date: 2026-08-13
- Scope: `engine/tool` (`Environment`, `EnvironmentForker`, `EnvironmentMerger`, `Tool.Execute`, `CommandRunner`/`CommandStreamer`); `engine/session` (`EnvironmentRef`, `EnvironmentKind`); `internal/adapter/forker`; per-session Environment ownership in `internal/adapter/server`; ACP/no-fs override construction in `internal/adapter/acp`
- Supersedes: [ADR 0104](./0104-execution-environment.md) — ONLY for the staged deferral of the `Environment`/runner/forker/merger runtime signatures (ADR 0104 decisions 1–3, which deliberately left the runtime seam unimplemented). ADR 0104's version-aware file mutation (decision 4–6) remains authoritative and is NOT superseded.
- Superseded by: [ADR 0106](./0106-environment-persistence.md) — ONLY for decision 6 (the phase-3 deferral of `EnvironmentRef` snapshot persistence and reattachment). This ADR's runtime seam (decisions 1–5) remains authoritative and is NOT superseded.

## Context

ADR 0104 established the execution-environment *direction* and shipped the
version-aware file-mutation protocol, but deliberately deferred the runtime seam:
it stated the future `tool.Environment` type would be added "only when a concrete
second execution backend proves the seam," and left `Tool.Execute` taking a bare
`tool.Workspace` with Bash closing over a separately injected `CommandRunner`.

That deferral left a real correctness gap: the filesystem a tool reads through and
the namespace a bound command runner executes in were selected *independently*. A
fork could merge changes from a namespace the child never executed in; a worktree
session could read one tree while Bash built another. The bound-runner model
already existed in composition (a `CommandRunner` was constructed against one
root), but the per-call `workdir` parameter on `CommandRunner.Run` let a caller
point a runner at a *different* root than the `Workspace` handed to the same
`Tool.Execute` — the affinity was a caller discipline, not a structural guarantee.

The in-process fix does not require a remote backend, a transport, or durable
reattachment. It requires only that the runtime value a `Tool.Execute` receives
bundles the affined filesystem and command namespace as one immutable unit, and
that a fork produces a complete child unit (not just a child `Workspace`).

## Decision

### 1. Add the in-process `tool.Environment` runtime seam

`Tool.Execute` now takes a `tool.Environment` (not a bare `tool.Workspace`). An
`Environment` is the immutable, per-namespace capability bundle:

- a `session.EnvironmentRef` naming the backend family (`Kind`) and an opaque
  backend identity (`ID`);
- a NON-NULL `Workspace` (the rooted, path-scoped filesystem seam — every tool
  reads through it, and the agent-facing Edit/Write enforce their
  read-before-edit / conditional-mutation invariants through it); and
- an OPTIONAL bound `CommandRunner` (present when the host has a shell for this
  namespace — the main session, a worktree or force-copy fork child; absent for a
  file-less / in-memory / shell-less namespace). The Bash tool reads it off the
  `Environment`; a nil runner surfaces `ErrNoShell`.

It is NOT a service locator: it carries no policy, hooks, MCP, memory, forker, or
merger. Those stay on the engine's `Deps` / the composition root. A fork
constructs a fresh child `Environment` bound to the child namespace
(`EnvironmentForker`); the parent `Environment` is never mutated.

### 2. Bind the command runner to one namespace at construction

`CommandRunner.Run` and `CommandStreamer.RunStreaming` LOSE the per-call `workdir`
parameter. A runner is bound to a single namespace at construction; the command's
cwd always matches the `Workspace` the tool executes against. This makes the
affinity structural: there is no API path by which a runner can be pointed at a
root different from the `Environment`'s `Workspace`.

### 3. Replace `WorkspaceForker`/`ForkMerger` with `EnvironmentForker`/`EnvironmentMerger`

`tool.EnvironmentForker.Fork` returns a COMPLETE child `Environment` — `Workspace`
AND a `CommandRunner` bound to the child namespace AND a child ref — so a forked
child's Bash observes the SAME child namespace its Read/Write do. `tool.
EnvironmentMerger.Merge` receives the child and parent `Environment`s (no
`forkRoot` string crosses the core interface). The old `WorkspaceForker`/
`ForkMerger` interfaces are removed; the two seams are not maintained in parallel.

The forker's `childEnv` derives the child ref ID and the bound runner root from
`ws.Root()` — the `Workspace` opened over the isolated directory is the single
source of truth for the child namespace. This is structural: on the
worktree-failure→copy fallback, the copy lives in a FRESH directory distinct from
the one `Fork` initially reserved; deriving both from `ws.Root()` guarantees
Workspace, Ref, and runner always share the actual copy root, never the discarded
reservation.

### 4. Environment-shaped ownership in the Service (not workspace-shaped heuristics)

The Service stores per-session `Environment` overrides (not bare `Workspace`
overrides) and exposes `SetSessionEnvironment`. An override is a COMPLETE
shell-less (or shell-bearing) `Environment` the creator owns: the creator supplies
the accurate ref (`Kind`/`ID`) and the correct `CommandRunner` (nil for a
file-less/buffer namespace). The Service does NOT guess a ref or runner from an
override's presence — every override carries its own truthful identity. The ACP
adapter registers a complete shell-less `Environment` with a LOCAL ref (a real-FS
workspace rooted at the session cwd, no shell); the no-fs profile registers a
complete shell-less `Environment` with a NOFS ref. The default local/worktree
paths are still constructed by composition from the existing `Workspace` + bound
runner factories.

### 5. `EnvironmentRef` is a cycle-safe in-process identity in phase 2

`session.EnvironmentRef{Kind, ID}` lives in `engine/session` (not `engine/tool`)
so it can ride the session snapshot and event log without pulling `tool` types in
— `tool` already imports `session` for `Tool.Execute`, so the identity half stays
inward of the runtime half. In phase 2 it is an IN-PROCESS identity only: it names
the backend family and an opaque backend identity string the adapter that minted
the `Environment` owns. The `session` package stores and carries it but interprets
NEITHER field. `EnvironmentKind` is a free-form string label (the closed set lives
in the tool layer and composition); the empty string is the zero "unspecified" ref.
Well-known labels (`EnvKindLocal`/`EnvKindMem`/`EnvKindNoFS`) cover the in-tree
adapters; a future remote transport adds its own label without widening the
package. An ACP-kind label may be added without being interpreted in `session`.

### 6. Defer snapshot persistence and remote transport to phase 3

This ADR implements the RUNTIME seam only. It does NOT persist `EnvironmentRef`
on the session snapshot, does NOT rehydrate a live `Environment` from a persisted
ref across a process restart, and does NOT choose an RPC, lease, upload protocol,
path syntax, credential forwarding, backend CAS token, or cleanup ownership for a
remote execution service. A restored session re-derives its `Environment` through
the existing rehydration path (the per-session factory rebuilds the engine and,
for no-fs, re-registers the override); the ref is not yet a reattachment input.
No stdio MCP or command-spawning transport is implied. A remote service must
provide true backend compare-and-swap and explicit lifecycle semantics when
designed; local adapters continue to make only the narrower same-live-Workspace
guarantee ADR 0104 states.

## Consequences

**Benefits:**

- Filesystem and command namespace are affined by construction: there is no API
  path by which a tool's `Workspace` and its `CommandRunner` point at different
  roots.
- A forked child's Bash observes the child namespace its Read/Write do — the
  forker owns the bound runner, and the per-call `workdir` is gone.
- The Service no longer guesses a ref or runner for an override; every override
  carries its own truthful identity, so an ACP session is correctly a LOCAL
  shell-less namespace, not a mislabeled no-fs one.
- The runtime seam is minimal and in-process: no speculative remote protocol, no
  persistence of live handles, no layering change (the ref stays in `session`,
  the capabilities stay in `tool`).

**Costs and limits:**

- `Tool.Execute`, `CommandRunner.Run`/`CommandStreamer.RunStreaming`,
  `Engine.Run`/`ResumeApproval`, and the forker/merger signatures change
  incompatibly; every implementation and fake must migrate (recorded in
  `engine/CHANGELOG.md`).
- `EnvironmentRef` is NOT yet durable: a process restart does not reattach a live
  `Environment` from a persisted ref. The rehydration path re-derives the engine
  and (for no-fs) the override; an ACP override (which closes over a live editor
  connection) does not survive a restart and is re-registered on reconnect. This
  is honest for phase 2 and is the deferred work of phase 3.
- A remote execution backend still requires true backend CAS and explicit
  lifecycle semantics; the in-process seam does not provide them.

## See also

- [ADR 0104 — Execution environments and version-aware file mutation](./0104-execution-environment.md)
  — the version-aware file-mutation protocol (decisions 4–6) remains
  authoritative; this ADR supersedes ONLY its staged deferral of the runtime seam
  (decisions 1–3).
- [Architecture — ports and adapter boundaries](../architecture.md)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md)
- [ADR 0027 — cloud-native state and resource inventory](./0027-cloud-native.md)
