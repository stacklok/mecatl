# ADR 0374 — Server-created session worktrees and stop-and-delete

- Status: Proposed
- Date: 2026-10-02
- Scope: local session placement creation, worktree placement identity, and session deletion
- Supersedes: [ADR 0291](0291-server-owned-session-placement.md), only Decision 1's
  "default `CreateSession` accepts only omitted or no-FS placement" rule and Decision 2's
  revision-mismatch rule for local worktree refs; [ADR 0032](0032-worktree-binding.md),
  only its "worktrees are discovered, never created" posture

## Context

Issue [#2058](https://github.com/stacklok/mecatl/issues/2058) asks mecatui to run several
sessions in one window, start each one in its own worktree, and delete a session when it
is done. Three server rules block that:

1. ADR 0291 lets `CreateSession` bind only the deployment default or no-FS, and lets only
   `ClearSession`/`ForkSession` move a session to an *existing* worktree. Nothing can create
   a worktree for a session. A remote client cannot run `git worktree add` itself, and ADR
   0291 forbids it from sending a path.
2. A worktree placement binds `EnvironmentRef{Kind: local, ID: <path>, Revision: <HEAD>}`,
   and `Reattach` requires HEAD to still match (`internal/app/placement.go`
   (`localPlacementProvider.currentWorktree`)). The first commit in that worktree makes
   every later run, `ListWorktrees`, and `ListCommands` fail closed. A long-lived session
   per worktree is impossible even with existing worktrees.
3. `DeleteSession` refuses running and awaiting sessions, and an awaiting session whose
   owner process died cannot be cancelled (`CancelRun` returns no active run). Deletion
   never touches a worktree.

## Decision

### 1. `CreateSession` may request a fresh server-created worktree

`CreateSessionRequest.new_worktree` is a server-owned *intent*, not a placement: the client
sends no path, name, branch, or selector. When set, the local placement provider creates a
new worktree of the deployment's configured repository and binds the session to it. It is
admitted only when the server could also list worktrees (configured workspace, shell, and
trusted project; [ADR 0095](0095-root-aware-project-trust.md)) and the default placement is
local; otherwise the request fails with failed precondition and creates nothing.
`ServerCapabilities.create_worktrees` reports admission. The session is an ordinary fresh
root: it records present, empty workspace-enrollment provenance exactly as any other fresh
`CreateSession` does ([ADR 0358](0358-durable-workspace-enrollment-broker-authority.md)
Decision 1).

The provider owns location and naming. Creates are serialized per repository. It runs
`git worktree add -b mecatl/<name> <managed-root>/<name> HEAD` from the configured root,
with the forker's hardened git environment (`gitenv.Scrub(envscrub.Scrub(...))`, hooks and
fsmonitor disabled). `<name>` is `mecatl-` plus eight random lowercase hex characters; a
collision with an existing path *or* branch is retried with a new name, a bounded number of
times. An unborn HEAD fails with failed precondition. The managed root is outside the
repository, at `$XDG_STATE_HOME/mecatl/worktrees/<repo-name>-<first 8 hex of sha256(root)>`
on the server host, so recursive tools in the main checkout never descend into it. A failed create
removes any partial worktree and branch and persists nothing.

Repository-configured git programs that hooks/fsmonitor settings do not cover (for example
`.gitattributes` filter drivers) can still run during checkout, status, and removal. That
is accepted under the same trust decision that already admits the read-only subagent
worktree shell over the repository's `.git` (ADR 0095); every server git call in this ADR
is behind that gate.

### 2. Local worktree identity survives commits

New local worktree binds use `Revision: "worktree-v1"`. `Reattach` of a local ref whose ID
is not the configured root matches by path: the path must still be a worktree listed by
`git worktree list` for the configured repository, and the revision must be either
`worktree-v1` or a well-formed hex git object id (a ref persisted before this decision).
Any other revision fails closed. Branch and HEAD remain display metadata read at bind/list
time. The configured-root ref and every non-local kind keep exact revision matching.

### 3. Ownership is derived, not persisted

A worktree is *server-created* exactly when the symlink-resolved path is a direct,
non-symlink child of the symlink-resolved managed root for the configured repository, and
git lists that resolved path as a worktree. No snapshot, event, or proto field records
ownership, so there is nothing to migrate or forge.

### 4. Delete may stop the session and remove its worktree

`DeleteSessionRequest` gains `stop_active` and `remove_worktree`.

`stop_active` lets deletion proceed on a running or awaiting main session. Before it
cancels or discards anything, the server takes the session's run-entry lock and then the
real session lease, the same proof of exclusive ownership `StartRunContent` uses. A lease
held by another process fails with failed precondition; delegation-child ids
(`subagent-`/`parallel-`/`team-`) are refused. A live run is cancelled and awaited for at
most 10 seconds; on timeout the request fails with deadline exceeded and nothing is
deleted. A parked awaiting ask, including one with no live owner after a restart, is
discarded with the session.

`remove_worktree` is admitted only for a server-created worktree that nothing else binds.
*Shared* means any other persisted session of any kind or owner, or any schedule, whose
local ref ID is the same resolved path. The server holds a per-path lock from the first
check until removal, so no Create, Clear, or Fork can bind the path in between. The order
is:

1. Check ownership, sharing, and cleanliness (no uncommitted or untracked changes). A
   failure returns failed precondition and changes nothing.
2. Stop the session if `stop_active` is set.
3. Re-check sharing and cleanliness. A failure here (for example, the run wrote a file
   before it was cancelled) still deletes the session, keeps the worktree, and returns
   `worktree_removed: false` with a reason.
4. Delete the session.
5. Run `git worktree remove` (never `--force`). A failure keeps the worktree and returns
   `worktree_removed: false` with a reason.

The `mecatl/<name>` branch is always kept, so committed work is never lost. Ignored files
in the worktree (build output, `.env`) are deleted with it. A process that dies between
steps 4 and 5 leaves the worktree on disk with no session; it is not swept and stays
visible to `git worktree list` for the user to remove.

## Consequences

- One client can create, run, and delete per-session worktrees without path authority.
- A worktree session no longer breaks on commit. The cost is weaker drift detection for
  local worktrees: a path that is still a worktree of the same repository reattaches even
  if someone replaced its checkout. The trust gate on worktree listing is unchanged.
- Server-created worktrees accumulate under the managed root until their session is
  deleted with `remove_worktree`. Deleting without it, or a crash mid-delete, leaves the
  worktree and branch on disk for the user to clean up.
- The sharing check scans persisted sessions and schedules, so deletion with
  `remove_worktree` costs a store scan.
- The managed root and the per-repository create lock are new outlives-a-call resources
  and get rows in the ADR 0027 inventory.

## See also

- [ADR 0291](0291-server-owned-session-placement.md) and [ADR 0032](0032-worktree-binding.md)
- [ADR 0297](0297-active-clear-cancellation-boundary.md), the abandon-and-replace precedent
  for acting on an active session
- [Multi-session mecatui window acceptance plan](../acceptance/tui-multi-session.md)
