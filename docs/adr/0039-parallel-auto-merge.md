# ADR 0039 — Parallel single-branch auto-merge

- Status: Proposed
- Date: 2026-06-21
- Scope: `engine/agent` (the Parallel tool), `engine/tool` (the `ForkMerger` port), `internal/adapter/forker` (the `Merger` adapter), `internal/app` (composition wiring), `cmd/mecated` (the `--parallel-auto-merge` flag)
- Supersedes: none
- Superseded by: none

## Context

The Parallel tool's historical contract is **no auto-merge** (the forker doc-comment
at `internal/adapter/forker/forker.go` and the Parallel spec text both state it).
A Parallel branch MAY mutate its own fork (Edit/Write/Bash), and for `join=first` /
`join=judge` the WINNER's fork is PRESERVED (its cleanup is dropped) so the operator
can inspect or merge it manually. For `join=all` every fork is torn down after the
join.

This is the right boundary for fan-out — N concurrent mutating branches should not
all be auto-landed into the parent — but it leaves a gap for the **single-branch
implement-and-land** case: an operator who delegates ONE implementation task to a
Parallel branch (`join=first`, one task) gets a preserved fork back, not edits in
their workspace. The model then has to either copy files out of the fork manually
(fragile shell composition against a temp path) or re-implement the work itself,
which defeats the point of delegation. In practice the model repeatedly hit this
and concluded "the parallel branches wrote the files but those were cleaned up
after the join" — sometimes correctly (it had used `join=all`, which DOES tear
down every fork before rendering), sometimes because the no-merge boundary was
opaque.

A separate, compounding bug made it worse: `join=all` printed `workspace: <path>`
lines for branches whose forks had ALREADY been torn down — dead paths the model
chased. That bug is fixed independently (the `joinBranches` renderer no longer
prints workspace paths for torn-down forks; the spec text now says `join=all`
tears down every fork). This ADR is about the structural gap: how does a
delegated implementer's edits LAND.

## Decision

Add an OPT-IN, SINGLE-BRANCH-only auto-merge fast path to the Parallel tool.

1. **A new `tool.ForkMerger` port** (`engine/tool/isolation.go`, next to
   `WorkspaceForker` — same layering reasoning): `Merge(ctx, forkRoot, parentWS)
   error`. Additive interface; no frozen domain type changes.

2. **A `forker.Merger` adapter** (`internal/adapter/forker/forker.go`) that
   implements `ForkMerger` by mirroring `overlayDirty`'s diff/apply mechanism in
   the reverse direction (fork → parent): `git diff --no-ext-diff --binary HEAD`
   from the fork piped to `git apply --whitespace=nowarn -` in the parent, plus
   copying the fork's untracked non-ignored files. Same scrubbed/neutralizing env
   as every other forker git invocation.

3. **A `WithAutoMerge(tool.ForkMerger)` option** on `ParallelTool` (`engine/agent`).
   When wired AND the run is a SINGLE-BRANCH `join=first` with a successful
   winner, the tool calls `merger.Merge` AFTER preserving the winner and BEFORE
   returning. On success the result notes the auto-merge; on conflict it returns
   a tool error naming the conflict + the preserved fork path (the fork is left
   intact for manual resolution). `ParallelTool.ReadOnly()` stays `true` — the
   merge is a POST-RUN step, not a dispatch-time mutation, so read-parallel /
   mutate-serial is unaffected.

4. **Composition wiring behind a flag** (`internal/app/catalog.go`):
   `cfg.ParallelAutoMerge` (default OFF) wires `forker.NewMerger()` via
   `WithAutoMerge`. The flag is exposed as `--parallel-auto-merge` on `mecated`
   (default false). `mecatui` and `mecatequi` leave it OFF (the zero value).

5. **Scope:** multi-branch runs NEVER auto-merge (the no-auto-merge boundary
   stays for fan-out). `join=judge` and `join=all` NEVER auto-merge. Only a
   single-branch `join=first` winner is eligible.

## Consequences

**Easier:** a delegated implementer's edits land in the parent workspace without
a manual copy/merge step — the "everything through sub-agents" workflow the
operator wants becomes viable for the single-branch implementation case. The
field-aligned model (Claude Code default, Codex, Goose: mutating agents edit the
shared tree) is now reachable for the single-branch case without abandoning
Parallel's isolation-for-fan-out story.

**Harder:** the no-auto-merge boundary is no longer absolute — it is now
"no auto-merge for fan-out; opt-in auto-merge for the single-branch fast path."
The ADR exists to record that this is a deliberate, scoped exception, not a
silent behavior change. Operators who do not set the flag see byte-identical
behavior (the merger is nil, the merge call is skipped).

**Committed to:**
- The merge runs in the PARENT workspace under the PARENT's trust posture, not
  the fork's. Applying a diff is a parent-side operation (the same trust the
  parent's own Edit/Write carries). The fork's content is untrusted
  child-authored data, but the parent already trusts its own Edit/Write surface.
- On conflict, NEVER force. The merge surfaces a tool error and PRESERVES the
  fork for manual resolution. A partial apply is left in place (the operator
  sees the exact failure state).
- The `ForkMerger` port is additive and lives in `engine/tool` (no `port↔tool`
  cycle). The `Merger` adapter is composition-injected; `engine/agent` imports
  no adapter.

**Costs:** a new port + adapter + option + flag + the `git diff`/`git apply`
invocations at merge time (one `git status`, one `git diff`, one `git apply`, one
`git ls-files` per merge — the same cost as `overlayDirty`). The merge is
git-dependent (a non-git workspace degrades to the no-auto-merge path: the
`status` probe fails and `Merge` returns an error naming the fork path).

## See also

- [Architecture guide](../architecture.md) — the Parallel section (to be updated
  to describe the auto-merge fast path).
- `docs/design/IMPLEMENTATION-NOTES.md` — the delegation section (to be updated).
- `internal/adapter/forker/forker.go` (`Merger`, `mergeForkInner`) — the adapter.
- `engine/tool/isolation.go` (`ForkMerger`) — the port.
- `engine/agent/parallel.go` (`WithAutoMerge`, `executeFirst` auto-merge block) —
  the tool wiring.
- `internal/app/catalog.go` (`registerParallelTool`) — the composition wiring.
- The `joinBranches` renderer fix (independent bug: `join=all` no longer prints
  dead workspace paths) and the Subagent spec reword (honest about the discarded
  worktree) — both in the same change set as this ADR.
