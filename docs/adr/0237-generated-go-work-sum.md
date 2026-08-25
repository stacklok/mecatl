# ADR 0237 — Treat `go.work.sum` as generated workspace metadata

- Status: Accepted
- Date: 2026-08-25
- Scope: Repository Go workspace dependency metadata
- Supersedes: ADR 0036's decision to commit `go.work.sum`
- Superseded by: —

## Context

This repository intentionally commits `go.work` so its independently released
modules can be developed together. The Go command regenerates `go.work.sum` when
the combined workspace graph needs checksums not present in an individual
member's `go.sum`.

That generated checksum file changes as member dependency graphs change. It has
created incidental dirty worktrees during ordinary builds; CI already discards
those changes before switching branches. The member `go.mod` files select
versions, while their committed `go.sum` files remain the source-controlled
integrity records for each published module.

## Decision

Keep the shared `go.work` committed. Stop tracking `go.work.sum` and ignore the
root-level generated file.

## Consequences

- Contributors can run workspace-aware Go commands without incidental
  `go.work.sum` changes appearing in their commits.
- Workspace-specific checksums are regenerated locally as required; a clean
  checkout may therefore become dirty in an ignored file after Go commands.
- Published module dependency selection and their committed `go.sum` integrity
  records remain unchanged.

## See also

- [ADR 0036](./0036-engine-module.md) — the retained committed-workspace decision.
- [ADR 0093](./0093-provider-modules.md) — provider submodules resolved locally by
  the workspace.
- [Go Modules Reference](https://go.dev/ref/mod#workspaces) — workspace semantics.
