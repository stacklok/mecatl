# ADR 0033 — Dirty-aware read-only fork (uncommitted-state overlay)

- Status: Accepted
- Date: 2026-06-18
- Scope: read-only fork isolation (Subagent + read-only team members), dirty-state mirroring into the child worktree
- Supersedes: none
- Superseded by: none

## Context

A read-only Subagent child, and a read-only team member, that carry a Bash shell
fork into a git worktree via `git worktree add --detach HEAD` (the forker's default
mode — see [ADR 0014](./0014-agent-teams.md) and `docs/architecture/parallelism.md`).
That is a CLEAN checkout of the committed `HEAD`: the child's whole workspace —
Read/Grep/Glob AND Bash — sees a clean tree. `git status` and `git diff` inside the
child report nothing even when the operator has uncommitted, staged, or untracked
work in the parent working tree.

The consequence is a silent capability gap: the most common reason to dispatch a
read-only explorer in a coding harness is to review the operator's IN-PROGRESS
changes ("look at what I've changed and tell me X"). The clean-HEAD worktree makes
that impossible — the explorer finds an empty diff and concludes there is nothing to
review. The operator's work is invisible to the very child meant to inspect it.

Two constraints shaped the fix:

1. **It must never break the fork.** The pre-fix clean-HEAD worktree is the safe
   floor; a half-applied overlay (some hunks applied, an apply error mid-stream) is
   strictly worse than a clean tree. The overlay has to be best-effort and fail back
   to pristine HEAD on any fault.
2. **It must preserve the harness git-hardening posture.** Every fork-time git
   invocation already runs with a scrubbed + neutralised environment
   (`gitenv.Scrub(envscrub.Scrub(os.Environ()))`) so a shared `.git/config`, a repo
   hook, a pager, or an external diff driver cannot drive code at fork time (the
   `runGit`/`gitRepoRoot` policy). Any NEW git invocation the overlay adds must carry
   the IDENTICAL environment.

The force-copy path (`WithForceCopy`, for MUTATING branches and members) does not
have this gap: `copyTree` already copies the parent's working tree verbatim,
including its uncommitted state. The gap is specific to the worktree branch.

The proven pattern already existed in the tree: `cmd/mecatequi/run.go`'s
`gitDiffPatch` computes a `git diff HEAD` + untracked-file enumeration to reproduce a
run's full working-tree delta. The overlay reuses that shape in reverse (apply into
the child rather than emit).

## Decision

Add an OPT-IN `forker.WithDirtyOverlay()` Option (NOT a default change). When set,
after `git worktree add --detach HEAD` succeeds and before the child workspace is
opened, the forker mirrors the parent's uncommitted state into the fresh worktree:

1. **Dirty probe** — `git status --porcelain`. Empty ⇒ clean ⇒ no-op. This keeps the
   common cheap path free (zero diff/apply overhead on a clean tree).
2. **Tracked + staged + deletions** — pipe `git diff --no-ext-diff --binary HEAD`
   from the parent into `git apply --whitespace=nowarn -` in the child. `diff HEAD`
   captures the net working-tree-vs-HEAD delta (exactly what `git status` reports);
   `--binary` round-trips binary files; deletions and renames-as-delete+add are
   reproduced by apply. (`git apply` does not accept `--no-ext-diff` — that is a
   diff-family flag — so it is applied to the diff side only; the scrubbed env
   already neutralises any external diff driver.)
3. **Untracked non-ignored** — `git ls-files --others --exclude-standard -z`, then
   copy each `<parentRoot>/<path>` → `<childDir>/<path>`, SKIPPING symlinks and
   irregular files (an `os.Lstat` check, mirroring `copyTree`'s discipline — a
   symlink could point outside the base and break isolation).

**Fail-soft, but SURFACED — not silent.** On any internal overlay error the forker
resets the worktree to pristine HEAD (`git checkout -- .` + `git clean -fd`); Fork
still succeeds with a valid clean worktree (the safe floor). But a silent fall-back
would leave a read-only explorer looking at a clean tree and reporting "nothing to
review" while the operator has uncommitted work — a quiet dishonesty. So the failure
is SURFACED to the child: `WorkspaceForker.Fork` returns an OPTIONAL degraded-fork
**advisory** (a generic, isolation-strategy-neutral string — empty in the normal
case) alongside the child workspace and cleanup. `overlayDirty` returns a non-empty
advisory whenever the dirty probe saw changes AND the overlay could not be applied
(it returns "" on a clean tree or a successful overlay). The Subagent tool PREPENDS
that advisory to the child LLM's prompt (composing with the resume-staleness note when
both apply), so the child reasons honestly — "the diff looks clean, but the operator
has uncommitted work I cannot see" — instead of silently concluding there is nothing
to review. The advisory is informational, never load-bearing for safety. The Parallel
branch (force-copy, never degrades) and the team read-only-member path discard it; the
member path doing so is a deliberate, documented scope boundary (the read-only Subagent
was the user's case), not a silent omission.

**Scrubbed env.** A new `runGitCapture(ctx, dir, stdin, args...)` sibling of `runGit`
carries the IDENTICAL `gitenv.Scrub(envscrub.Scrub(os.Environ()))` env line, captures
stdout, pipes `stdin` when non-nil, and folds stderr into the error. A `splitNUL`
helper parses the `-z` output (kept byte-for-byte identical to the sibling parser in
`cmd/mecatequi/run.go`; deliberately NOT extracted to a shared helper until a third
`-z` parser appears — Rule of Three, different layers).

**Composition wiring.** `internal/app/build.go` wires `WithDirtyOverlay()` onto the
two read-only worktree forkers: the Subagent child forker (`buildSubagentTool`) and
the read-only team-member forker (`roFk` in `buildTeamWiring`). The mutating
force-copy forkers (`fk`, the Parallel branch forker) are UNCHANGED — they already
carry dirty state via `copyTree`, and `WithForceCopy` wins by call site so the
overlay never runs on that path.

## Consequences

- A read-only Subagent or team member dispatched over a dirty workspace now sees the
  operator's uncommitted, staged, and untracked work — the headline capability fix.
- The overlay adds git invocations (`status`, `diff`, `apply`, `ls-files`) on the
  fork path ONLY when the tree is dirty. A clean tree pays a single `status
  --porcelain` probe and nothing more.
- The overlay is best-effort: a pathological repo state where `git apply` cannot
  reproduce the diff degrades to a clean-HEAD worktree (the pre-fix behaviour), never
  a hard fork failure and never a partial overlay. The degradation is no longer
  invisible — the child is TOLD via the degraded-fork advisory that it is seeing
  committed HEAD only, so it does not silently mis-report a clean-looking tree.
- `WorkspaceForker.Fork` gained a return value (the advisory). This is a small
  domain-interface widening (a generic degraded-fork channel, not overfit to the
  overlay); every existing implementation and caller — the real forker, the Parallel
  and team paths, and the test fakes — was updated, and non-overlay paths return "".
- **Known limitation — submodules.** The overlay mirrors the parent's working-tree
  delta and untracked files; an uncommitted change to a SUBMODULE POINTER (gitlink)
  rides the `diff HEAD` patch as a gitlink update, but the submodule's own working
  tree is not recursively overlaid. This is best-effort by design — a read-only
  explorer inspecting submodule contents is out of scope for this round.
- The isolation guarantee is unchanged: the child writes only into its own worktree;
  the parent working tree and index are never touched by the overlay (the overlay
  reads the parent and writes the child).

## See also

- [ADR 0032 — First-class worktree binding](./0032-worktree-binding.md): the
  session/worktree binding whose `git worktree` + scrubbed-env posture this reuses.
- [ADR 0023 — Workspace Trust](./0023-workspace-trust.md): the trust model; the
  read-only-member shell remains trust-gated, and the overlay adds no new ingress.
- [ADR 0014 — Agent teams](./0014-agent-teams.md): the read-only-member worktree
  isolation this overlay completes.
- `docs/architecture/parallelism.md`: the forker section (the living "how it works").
- [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md): the forker/subagent worktree subsystem notes.
- [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md): the fork-join / worktree shipped-status row.
