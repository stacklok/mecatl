# ADR 0315 — Add POSIX-like workspace namespace operations

- Status: Accepted
- Date: 2026-09-03
- Scope: Workspace capabilities, built-in filesystem tools, local and remote workspace adapters
- Supersedes: None
- Superseded by: None

## Context

Mecatl's workspace seam supports safe content reads and version-aware file replacement, but it cannot list one directory, remove a path, rename a path, or copy a file without falling back to Bash. That fallback is unavailable in shell-less deployments such as Redis workspaces and grants much more capability than the requested filesystem operation.

Adding these methods directly to `tool.Workspace` would break every external workspace implementation. The operations also have different concurrency semantics from Edit and overwrite-Write: they change namespace entries rather than conditionally replacing content derived from a prior read.

## Decision

Add an optional `tool.WorkspaceNamespace` capability with `ReadDir`, `Remove`, `Rename`, and `CopyFile`. Keep `tool.Workspace` unchanged so existing embedders remain source-compatible. Built-in `ListDir`, `Remove`, `Move`, and `Copy` tools type-assert the capability and return a model-visible unsupported error when it is absent.

Make deletion non-recursive, refuse destination replacement for move and copy, and restrict copy to regular files. Namespace operations do not consult or update the read ledger: no operation derives replacement content from a prior read, while no-clobber semantics protect destination names. `ListDir` is read-only; the other three tools are mutating and therefore dispatch serially and are denied in plan mode.

Authorize both source and destination independently for `Copy` and `Move`. Implement the capability for local OS, memory, Redis, and remote test workspaces. Prefix-backed stores derive directories from file keys and therefore cannot preserve empty directories. ACP exposes the capability it can support and returns the shared unsupported sentinel for protocol operations ACP cannot express.

## Consequences

Agents can perform narrow namespace operations without a shell, including in Redis-backed deployments. Redis move, copy, and remove operations are atomic through Lua scripts, and all implementations share a conformance suite.

The interface is additive rather than uniformly guaranteed: custom workspaces may omit it, and callers must handle unsupported operations. Prefix-backed workspaces intentionally differ from POSIX filesystems around empty directories. Move and copy are deliberately less permissive than POSIX `rename(2)`/`cp`: they never overwrite a destination, trading convenience for model-safe behavior.

## See also

- [Architecture guide](../architecture.md#2-the-big-picture)
- [ADR 0208 — Execution environment and versioned workspace protocol](./0208-execution-environment.md)
- [ADR 0298 — Persistent read-before-write ledgers](./0298-persistent-read-before-write-ledgers.md)
