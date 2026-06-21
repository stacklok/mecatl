# ADR 0039 — Parallel single-branch auto-merge

- Status: Accepted
- Date: 2026-06-21
- Scope: `engine/agent` (the Parallel tool), `engine/tool` (the `ForkMerger` port), `internal/adapter/forker` (the `Merger` adapter), `internal/app` (composition wiring)
- Supersedes: none
- Superseded by: 0040

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

An earlier draft of this decision made auto-merge OPT-IN behind a
`--parallel-auto-merge` flag (default OFF). That was revised to DEFAULT-ON with no
flag during review: for a single-branch winner there is no fan-out, so the
no-auto-merge boundary (which exists for fan-out) does not apply — throwing a
single winner's edits away (or handing the operator a temp path to manually merge)
is the broken UX that prompted the whole change. An opt-in flag for "do the right
thing for one branch" is counter-intuitive. The boundary that matters —
multi-branch runs never auto-merge — is preserved by the `len(results) == 1`
eligibility gate, not by a flag.

## Decision

Auto-merge a SINGLE-BRANCH winner's diff back into the parent workspace by
DEFAULT — no operator flag.

1. **A new `tool.ForkMerger` port** (`engine/tool/isolation.go`, next to
   `WorkspaceForker` — same layering reasoning): `Merge(ctx, forkRoot, parentWS)
   error`. Additive interface; no frozen domain type changes.

2. **A `forker.Merger` adapter** (`internal/adapter/forker/forker.go`) that
   implements `ForkMerger` by mirroring `overlayDirty`'s diff/apply mechanism in
   the reverse direction (fork → parent): `git diff --no-ext-diff --no-textconv
   --binary HEAD` from the fork piped to `git apply --whitespace=nowarn -` in the
   parent, plus copying the fork's untracked non-ignored files. Same
   scrubbed/neutralizing env as every other forker git invocation. SECURITY: the
   `--no-textconv` flag is load-bearing — the fork's `.git/config` is
   attacker-authored (a force-copy branch has write to its own `.git`), and an
   attacker-installed `diff.<drv>.textconv` would otherwise fire during `git diff`,
   executing an attacker-chosen command on the parent host at merge time and
   letting its stdout masquerade as the merged content. `--no-ext-diff` suppresses
   only `diff.external`; `--no-textconv` suppresses `diff.*.textconv` (verified).
   The merge ALSO refuses any patch that touches `.gitattributes` (an untrusted
   branch must not silently repoint the parent's git filter/diff drivers — a
   staged `.gitattributes` adding `filter=<name>` would cause the parent's
   `filter.<name>.smudge` to fire on attacker-controlled blob content at merge
   time).

3. **A `WithAutoMerge(tool.ForkMerger)` option** on `ParallelTool` (`engine/agent`).
   The shared `autoMergeWinner` helper fires for a SINGLE-BRANCH winner
   (`len(results) == 1`) of `join=first` OR `join=judge` — both single-branch
   winners land (paths (a) and (c) from the earlier draft collapse: a one-branch
   judge run and a one-branch first run are the same "delegate and land" case). It
   calls `merger.Merge` AFTER preserving the winner and BEFORE returning. On
   success the result notes the auto-merge; on conflict it returns a tool error
   naming the conflict + the preserved fork path (the fork is left intact for
   manual resolution). `ParallelTool.ReadOnly()` stays `true` — the merge is a
   POST-RUN step (after `preserveWinner`, before `Execute` returns), not a
   dispatch-time mutation, so read-parallel / mutate-serial is unaffected.

4. **Composition wiring** (`internal/app/catalog.go`): `forker.NewMerger()` is
   wired via `WithAutoMerge` UNCONDITIONALLY (no flag, no Config field). The
   merger is a composition-owned adapter injected as a `tool.ForkMerger`;
   `engine/agent` imports no adapter.

5. **Scope:** multi-branch runs NEVER auto-merge (the no-auto-merge boundary
   stays for fan-out). `join=all` NEVER auto-merges (no winner). Only a
   single-branch `join=first`/`join=judge` winner is eligible.

## Consequences

**Easier:** a delegated implementer's edits land in the parent workspace without
a manual copy/merge step — the "everything through sub-agents" workflow the
operator wants is the DEFAULT for the single-branch case. The field-aligned model
(Claude Code default, Codex, Goose: mutating agents edit the shared tree) is now
the behaviour for the single-branch case without abandoning Parallel's
isolation-for-fan-out story. No operator flag to discover or set.

**Harder:** the no-auto-merge boundary is no longer absolute — it is now "no
auto-merge for fan-out; auto-merge for the single-branch fast path." The ADR
records that this is a deliberate, scoped exception. Operators who relied on the
old "single-branch winner's fork is preserved but NOT merged" behaviour see a
behaviour change: the winner's edits now land in the parent AND the fork is still
preserved (for inspection / the `--fork-preserved-cap` reaper). The
single-branch case is the common "delegate one implementation task" path, so
landing is the right default; the rare "I want the fork preserved but NOT merged"
case is not directly supported (the operator can `git reset` the parent after the
run, or avoid single-branch Parallel for that workflow).

**Committed to:**
- The merge runs in the PARENT workspace under the PARENT's trust posture, not
  the fork's. Applying a diff is a parent-side operation (the same trust the
  parent's own Edit/Write carries). The fork's content is untrusted
  child-authored data, but the parent already trusts its own Edit/Write surface.
- On conflict, NEVER force. The merge surfaces a tool error and PRESERVES the
  fork for manual resolution. A partial apply is left in place (the operator
  sees the exact failure state).
- SECURITY: the merge's `git diff` runs with `--no-textconv` (closes
  `diff.<drv>.textconv` RCE from an attacker-authored fork `.git/config`) and
  refuses `.gitattributes`-touching patches (closes `filter.<drv>.smudge` RCE
  via attribute repointing). `--no-textconv` is also applied to `overlayDirty`'s
  `git diff` for defence-in-depth parity. The `gitenv.Scrub` env remains
  defence-in-depth for the fixed keys; the two new mitigations close the
  attacker-named-driver class the env structurally cannot.
- The `ForkMerger` port is additive and lives in `engine/tool` (no `port↔tool`
  cycle). The `Merger` adapter is composition-injected; `engine/agent` imports
  no adapter.

**Costs:** a new port + adapter + option + the `git diff`/`git apply`
invocations at merge time (one `git status`, one `git diff`, one `git apply`, one
`git ls-files` per merge — the same cost as `overlayDirty`). The merge is
git-dependent (a non-git workspace degrades to an error: the `status` probe
fails and `Merge` returns an error naming the fork path).

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
