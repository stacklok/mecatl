# ADR 0041 — Direct-write writable Subagent (no fork, no merge-back)

- Status: Accepted
- Date: 2026-06-22
- Scope: `engine/agent` (the Subagent tool's `mode:"read-write"` path), `internal/app` (composition wiring of the writable child engine + the shared merger)
- Supersedes: the WRITABLE-SUBAGENT decision in [ADR 0040](./0040-writable-subagent-and-serialized-merge.md) ONLY (NOT 0040's Parallel single-branch auto-merge, nor the `parentMutatingCaller` dispatch-serial seam — both REUSED here)
- Superseded by: none

## Context

ADR 0040 shipped the writable Subagent (`mode:"read-write"`) as a FORCE-COPY fork plus
a serialized merge-back: a read-write call ran an Edit/Write-bearing child in a
full copy of the workspace (its own `.git`), and on a clean terminal the child's
working-tree diff was merged back into the parent via the shared `tool.ForkMerger`.

That design carried real costs that direct experience surfaced:

- **O(repo) copy per call.** A force-copy fork copies the entire working tree (and
  `.git`). On a large checkout — or in a container/kubernetes pod with a modest
  ephemeral disk — every `mode:"read-write"` delegation paid a full-tree copy before
  the child ran a single tool. For the common "implement this one fix" case that is
  almost all overhead.
- **The merge could lose everything.** A merge CONFLICT landed NONE of the child's
  work and preserved the fork for manual resolution — the model's whole delegated
  task became an error pointing at a directory, exactly when the edits were most
  wanted. A merge is an all-or-nothing gate the child never sees coming.
- **The inversion.** The main agent writes the real workspace directly; git is its
  safety net. The writable Subagent is meant to be "a focused version of you that
  edits files." Forcing it through a copy+merge made it BEHAVE differently from the
  thing it is modelled on, for no isolation benefit a serial-only writer needs: a
  `mode:"read-write"` call already runs ALONE (mutate-serial, via the
  `parentMutatingCaller` seam from ADR 0040), so there is no concurrent sibling to
  isolate it from.

The fork-and-merge posture is genuinely needed where there IS concurrency —
`Parallel` branches and mutating `Team` members run many writers at once over the
same base, so each needs its own copy and a serialized merge. Those are out of scope
here.

## Decision

A `mode:"read-write"` Subagent writes DIRECTLY to the real parent workspace, like the
main agent. No fork, no copy, no merge-back. This is the new DEFAULT and ONLY behaviour
— there is no flag; the per-call default stays read-only (the model opts into writes
per call via `mode:"read-write"`).

Concretely (`engine/agent/subagent.go`, `internal/app/build.go`):

- The writable child runs against the parent `tool.Workspace` it was handed —
  `prepareChildSession` passes a NIL forker for the writable path, so
  `forkChildWorkspace` returns the parent workspace directly. Its Edit/Write/Bash
  mutate the real tree in place.
- The child engine (`buildWritableSubagentChildEngine`) layers Edit/Write onto the
  read-only explorer catalog and uses the **MAIN session's command runner**
  (`buildCommandRunner`) — main-session parity. The force-copy runner was
  trust-UNGATED only because a force-copy fork has no fork-time git; running on the
  REAL workspace means a writable child's Bash must resolve exactly as the main
  session's does under the operator's posture/policy, so it uses the same runner the
  main session uses.
- `SubagentTool.MutatesParent` is the LOAD-BEARING correctness fix: a direct-write
  child mutates the real tree DURING its run, so the dispatcher MUST keep it
  mutate-serial (excluded from the concurrent read batch, flushed alone). It now
  returns true whenever the writable child engine is wired and the call is
  `mode:"read-write"` — DECOUPLED from any merger (there no longer is one). The
  `parentMutatingCaller` seam itself (ADR 0040) is REUSED unchanged.
- The writable child posture is `isolated:false` (it shares the real tree), so the A2
  isolation auto-approve (`governance.IsolationApprovable`) does NOT apply to its Bash
  — its Bash now hits the real repo, so it resolves at main-session parity instead.
- The result text honestly tells the model its edits were applied DIRECTLY to the
  workspace (review with `git diff`/`git status`, undo with `git checkout`/`git
  stash`); on a non-clean terminal it warns the edits may be PARTIAL.
- The two now-dead exported options `agent.WithWritableChildForker` and
  `agent.WithSubagentAutoMerge` are REMOVED (a clean break — see
  [`engine/CHANGELOG.md`](../../engine/CHANGELOG.md), classified breaking).
  `agent.WithWritableChildEngine` is retained.

The shared `tool.ForkMerger` (`internal/app/catalog.go`, `internal/app/build.go`) stays
for `Parallel`'s single-branch auto-merge, now its sole consumer; it is built only when
Parallel is enabled.

## Consequences

- **Cheaper.** No per-call full-tree copy; the writable child starts immediately
  against the real workspace.
- **Edits always land as they are made.** There is no merge gate that can lose the
  whole task on a conflict; the model sees its own edits in the real tree turn by turn.
- **Partial edits survive a crash.** A `mode:"read-write"` child that crashes
  (`StopError`) or is cancelled mid-run leaves its completed edits IN the working tree
  — there is no fork to quarantine and discard them. This is the accepted trade-off:
  git is the safety net (the result text says so, and warns the edits may be partial).
  The previous fork-and-merge posture's "a failed child lands nothing" guarantee is
  gone by design.
- **No isolation.** A writable child shares the real tree with the parent. It is
  dispatch-serial so it cannot race the parent's own tools, and `ReadOnly()` stays
  true for read-only fan-out — but its Bash now hits the real repo, so the A2
  isolation auto-approve is correctly removed (`isolated:false`). Its Bash/Edit/Write
  resolve at main-session parity under the operator's posture/policy.
- **Scope is the serial Subagent only.** `Parallel` branches and mutating `Team`
  members keep the force-copy fork + serialized merge — they have genuine concurrency
  that direct-write would race. Moving those onto git worktrees is possible future
  work, OUT OF SCOPE here.

## See also

- [ADR 0040](./0040-writable-subagent-and-serialized-merge.md) — the superseded
  fork-and-merge writable Subagent (its Parallel auto-merge and the
  `parentMutatingCaller` seam are reused, not superseded).
- [ADR 0014](./0014-agent-teams.md) — agent teams (the concurrent mutating members
  that still use force-copy).
- `docs/architecture/subagents-and-teams.md` — the living description of the subagent writable mode.
- `docs/design/IMPLEMENTATION-NOTES.md` — the per-subsystem writable-Subagent entry.
- The documentation lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
