# ADR 0104 — Execution environments and version-aware file mutation

- Status: Accepted
- Date: 2026-08-12
- Scope: future execution-environment identity and capability seams; `engine/tool` Workspace version protocol; `engine/adapter/{fstools,memfs,nofs}`; `internal/adapter/{osfs,acp}`; per-session Workspace lifetime in `internal/adapter/server`
- Supersedes: none
- Superseded by: [ADR 0105](./0105-execution-environment-runtime-seam.md) — for decisions 1–3 (the staged deferral of the `Environment`/runner/forker/merger runtime seam) ONLY. ADR 0104's version-aware file mutation (decisions 4–6) remains authoritative.

## Context

A coding agent does not act on an abstract filesystem and an unrelated shell. Its file operations and
commands must address the same place: a local checkout, an editor buffer namespace, a container, a
remote worker, or a fork. If the filesystem and command runner are selected independently, `Read` may
inspect one tree while `Bash` builds another, or a fork may merge changes from a namespace the child
never executed in. The correct unit is an **execution environment**: an identity with an affined
filesystem and command namespace.

The current public seam passes a `tool.Workspace` to tools while Bash closes over a separately injected
`tool.CommandRunner` (`engine/tool/tool.go`, `engine/adapter/fstools/bash.go`). Forking and merging are
already separate capabilities (`engine/tool/isolation.go`). Collapsing these into a large interface now
would widen the core API before remote ownership, lifecycle, transport, and durable reattachment are
understood. It would also put persistence in the wrong layer: `session` cannot import `tool`, because
`tool` already imports `session` for `Tool.Execute`.

There is an immediate correctness problem that can be solved without that speculative seam. The old
Edit ledger stored a fingerprint but adapters re-read during `RecordRead`; Edit then performed an
unconditional write after its last check. A cooperating mutation between that check and the write could
therefore be silently overwritten.

## Decision

### 1. Treat execution environment as the future unit of coding-agent execution

A coding agent runs in one execution environment whose filesystem and command namespace are affined.
The future minimal runtime value belongs in `engine/tool` and is intentionally small:

- environment identity;
- a `Workspace`; and
- a `CommandRunner` already bound to that Workspace's command namespace.

The future type is conceptually `tool.Environment`; this ADR does **not** add it yet. Tools, the agent
loop, and composition will migrate only when a concrete second execution backend proves the seam.
`WorkspaceForker` and `ForkMerger` remain separate optional capabilities rather than methods on the
minimal environment. Governance and permission policy remain outside the environment: an environment
states where an operation runs, not whether it is authorized.

### 2. Put durable identity in `session.EnvironmentRef`

When durable environment identity is introduced, the cycle-safe persisted reference belongs in
`engine/session` as `session.EnvironmentRef`. `session` must not import `tool`; the runtime
`tool.Environment` may carry that reference because `tool` already depends inward on `session`.
Snapshots and event-sourced rehydration persist only identity/reference data, never live Workspace,
runner, transport, credential, or process handles. Composition resolves the reference to live
capabilities at run entry.

No `EnvironmentRef` or `Environment` type is added in this PR. Their exact fields await a real durable
backend and reattachment contract.

### 3. Keep remote transport deferred

This decision does not choose RPCs, leases, upload protocols, path syntax, credential forwarding,
backend CAS tokens, or cleanup ownership for a remote execution service. A remote service must provide
true backend compare-and-swap and explicit lifecycle semantics when designed. Local adapters make only
the narrower same-live-Workspace/backend-handle guarantee below (with osfs explicitly stronger). No
stdio MCP or command-spawning transport is implied.

### 4. Begin the staged migration with version-aware file mutation

`tool.Workspace` gains an opaque `tool.FileVersion` and a version-aware mutation protocol
(`engine/tool/tool.go`):

- `ReadVersion` returns content and its authoritative version;
- `RecordRead(path, version)` stores that exact version with no I/O;
- `RecordedVersion(path)` is the I/O-free ledger lookup;
- `CreateFile` is create-only; and
- `ReplaceFile` replaces only the version supplied by the caller.

Ledger key normalization is also part of the no-I/O contract: adapters use only lexical
`filepath.Clean`/`filepath.Rel`-equivalent operations. Ordinary absolute `<root>/<rel>` and relative
`<rel>` forms converge; out-of-root absolute paths key by cleaned absolute spelling. Physical symlink
aliases may conservatively use distinct keys, causing a safe extra Read rather than hidden I/O in
RecordRead/RecordedVersion.

There is no empty-version or `AnyVersion` overwrite sentinel. Plain `Read` remains through the
read-only Workspace surface, but unconditional `Write` is removed from public `tool.Workspace`: the
capability passed to every `Tool.Execute` exposes only `CreateFile` and version-matched `ReplaceFile`
for mutation. Concrete adapters and their lower-level `FileSystem` may retain adapter-public Write
methods for bootstrap/test setup; those methods are not part of the Tool capability. The built-in
Read/Edit/Write tools use the version protocol (`engine/adapter/fstools/read.go`,
`engine/adapter/fstools/edit.go`, `engine/adapter/fstools/write.go`). New-file Write uses
`CreateFile`. Existing-file Write and Edit require a recorded version, compare it with a current
`ReadVersion`, then finish with `ReplaceFile` against that current version. The final conditional
replace is load-bearing: a mutation after the current read is returned to the model as the existing
changed-since-read refusal, never overwritten.

A `FileVersion` is opaque outside equality. Adapters mint it from their authority: osfs and memfs use a
content hash; ACP hashes the editor buffer returned by `fs/read_text_file`. A zero value is invalid and
never means "overwrite any version."

### 5. Define adapter atomicity honestly

`CreateFile` and the compare-plus-mutation in `ReplaceFile` are atomic with respect to concurrent
calls through the same live Workspace/backend handle:

- memfs uses its filesystem mutex for calls on its shared in-memory backend
  (`engine/adapter/memfs/memfs.go`);
- ACP uses its per-workspace-instance mutex (`internal/adapter/acp/fsworkspace.go`); and
- osfs deliberately provides a stronger guarantee: it derives the lock identity from the physical
  in-root target (full-target canonicalization when existing; canonical parent plus basename when
  missing), then serializes same-target mutations process-wide across Workspace instances with fixed
  lock striping. The stripe guarantee is PROCESS-SCOPED: it serializes only cooperating Workspace
  writers in this process. A non-cooperating writer (shell command, another process, an editor) does
  not participate and can still race the final write. The lock identity is computed BEFORE the lock is
  taken, so a non-cooperating writer that races target EXISTENCE or a SYMLINK IDENTITY during that
  pre-lock canonicalization step (e.g. swaps the file for a symlink between the Lstat and the
  Canonicalize) can cause a cooperating writer to lock a different identity than the bytes it
  ultimately writes; osfs does NOT re-resolve under the lock. The final confined write still flows
  through `os.Root`, which refuses a symlink traversal that escapes the root, but an in-root symlink
  swap race is not closed by the lock alone. CreateFile additionally uses `O_CREATE|O_EXCL`, so a
  non-cooperating creator cannot be overwritten (`internal/adapter/osfs/osfs.go`).

ACP decides that a missing read authorizes CreateFile only from the structured editor/RPC error.
Before classification it removes the exact already-confined requested path (bare or commonly quoted)
from that trusted message, normalizes harmless formatting, then accepts only anchored absence shapes:
`not found`, `file not found`, `no such file or directory`, `does not exist`, or `enoent`. It never
scans arbitrary marker substrings. Generic transport/decode/protocol failures and structured
permission/unavailable errors fail closed even when the echoed path contains a not-found marker
(`internal/adapter/acp/fsworkspace.go`). ACP's `fsWorkspace` splits its synchronization: a `ledgerMu`
guards ONLY the in-memory ledger (so ledger lookup/record never blocks behind a network call), and a
separate `callMu` serializes the buffer CAS / create RPC sequences in CreateFile/ReplaceFile
(`internal/adapter/acp/fsworkspace.go`).

The base contract does not claim that independent Workspace instances over an arbitrary backend are
globally serialized. Arbitrary POSIX writers that bypass the Workspace seam — shell commands, another
process, or an editor writing the same file directly — do not participate in local osfs CAS. They may
still race the bytes of the final write, and (as above) a race against target existence/symlink
identity during pre-lock canonicalization is not closed by the process-scoped lock. This is not
described as kernel-level atomic replacement. A future remote backend must provide true backend CAS,
which closes the gap by making the conditional replace atomic at the storage layer.

### 6. Scope the read ledger to the live Workspace instance

The read ledger lives on the Workspace/environment instance that produced the version. The default
Service path continues to construct a fresh Workspace for each run; `Service.sessionWorkspaces`
remains limited to existing owner-scoped overrides such as no-fs and ACP
(`internal/adapter/server/service.go`). Rebuilding the Workspace — on a later default user run or a
process restart — resets the ledger intentionally. The first mutation is refused until the model reads
the file through that live instance. No new Service registry, os.Root retention, close contract, or
cross-run authorization state is introduced.

## Consequences

**Benefits:**

- The Workspace capability handed to tools has no unconditional overwrite operation.
- A concurrent cooperating mutation after the tool's current read is detected by the final conditional
  replace.
- Create races fail instead of replacing the winner.
- The future environment direction has a cycle-safe identity location and a deliberately minimal
  runtime shape without prematurely fixing a remote protocol.

**Costs and limits:**

- `tool.Workspace` changes incompatibly; every implementation and fake must implement the version
  protocol.
- Content hashing reads full file bytes and treats byte-identical rewrites as the same version. That is
  correct for content-based Edit semantics but does not represent metadata changes.
- The in-memory ledger is scoped to the live Workspace instance. Rebuilding that instance, including
  the next default Service run or a process restart, requires a re-read by design.
- Local osfs CAS excludes non-cooperating writers; true backend CAS remains deferred.
- The future Environment and EnvironmentRef migration is still work. This PR deliberately does not
  change Engine execution signatures or bind Bash to a runtime Environment.

## See also

- [Architecture — ports and adapter boundaries](../architecture.md)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md)
- [ADR 0027 — cloud-native state and resource inventory](./0027-cloud-native.md)
- [ADR 0036 — engine module boundary](./0036-engine-module.md)
- [ADR 0077 — direct-write subagents](./0077-direct-write-subagent.md)
