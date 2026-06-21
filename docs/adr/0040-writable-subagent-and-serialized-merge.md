# ADR 0040 — Writable Subagent mode + serialized merge-back

- Status: Accepted
- Date: 2026-06-21
- Scope: `engine/agent` (the Subagent + Parallel tools, the dispatcher's per-call mutate-serial seam), `engine/tool` (the `ForkMerger` port doc), `internal/adapter/forker` (the `Merger` + new `SerializingMerger`), `internal/app` (composition wiring)
- Supersedes: 0039
- Superseded by: none

## Context

[ADR 0039](0039-parallel-auto-merge.md) made a SINGLE-BRANCH `Parallel` winner's
diff auto-merge back into the parent workspace, default-on, so a delegated
implementation task's edits land without a manual copy step. It justified
keeping `ParallelTool.ReadOnly() == true` with "the merge is a POST-RUN step …
not a dispatch-time mutation, so read-parallel / mutate-serial is unaffected."

Two problems surfaced after 0039 shipped:

1. **The single-task implement-and-land verb was the wrong tool.** `Parallel`
   exists for fan-out — N concurrent competing/independent branches. Routing
   "delegate ONE unit of work and land it" through a degenerate one-element
   `Parallel(join:"first")` is a conceptual misuse: the model has to reason
   about a fan-out tool with a single branch, and the dev-pipeline skill had to
   teach that misuse explicitly. `Subagent` — the natural "delegate one focused
   task" verb — was locked read-only (no Edit/Write; its worktree discarded),
   so it could not be that verb.

2. **The `ReadOnly() == true` justification was incomplete — a concurrency
   race.** The dispatcher (`engine/agent/dispatch.go`) batches consecutive
   `ReadOnly()==true` tool calls and runs them CONCURRENTLY against the same
   parent workspace. So two single-branch `Parallel(join:"first")` calls in one
   assistant turn land in the same read-batch and BOTH `Merger.Merge` (two
   concurrent `git apply`) into the SAME parent tree — a race (interleaved /
   partial applies). The merge being "post-run" from the child's view does not
   make it non-concurrent: it executes inside the dispatch goroutine, alongside
   the batch. The same exposure applies merge-vs-sibling-read: a merge rewriting
   parent files concurrently with a sibling `Read`/`Grep` of those files.

## Decision

**1. Add a writable Subagent mode (`mode: "read-write"`), and make it the
blessed single-task implement-and-land path.** A `mode:"read-write"` Subagent
call runs its child in a FORCE-COPY fork (own `.git`, like a Parallel branch /
mutating team member) with Edit/Write added to the read-only explorer catalog,
and on a CLEAN finish auto-merges the child's diff back into the parent
workspace via the shared `ForkMerger`. It is DEFAULT behaviour — wired
unconditionally with NO operator flag and NO `Config` field (the only
requirement is a real filesystem; the no-FS profile excludes it). The model
opts in PER CALL; the per-call default stays read-only (`mode:""` ==
`"read-only"`), so a delegation that should only investigate cannot silently
mutate the tree. `mode` is a closed set `{"", "read-only", "read-write"}`;
an unknown value is a model-addressable error.

**2. Serialize every merge-back two complementary ways.**

   - **Per-call dispatch-serial (within a run).** A new UNEXPORTED
     `parentMutatingCaller` interface (`MutatesParent(call) bool`) lets a tool
     that is `ReadOnly()==true` in general declare that a SPECIFIC call will
     mutate the parent on completion. The dispatcher excludes such a call from
     the concurrent read batch — it flushes ALONE, serially, exactly like a
     mutating tool — so a merge never overlaps a sibling read or another merge
     in the same run. `SubagentTool.MutatesParent` returns true for a
     `mode:"read-write"` call (when the writable engine + merger are wired);
     `ParallelTool.MutatesParent` returns true only for a single-branch
     `join=first/judge` call that will auto-merge. Read-only Subagent fan-out
     stays fully parallel — `ReadOnly()` is unchanged for those calls.

   - **Process-wide serialized merger (across runs).** A
     `forker.SerializingMerger` decorator wraps the `forker.Merger` behind one
     `sync.Mutex`, built ONCE in composition and injected into BOTH merge paths
     (`WithAutoMerge` for Parallel, `WithSubagentAutoMerge` for Subagent). This
     serializes merges across concurrent sessions/runs sharing one workspace,
     which the per-run dispatch seam cannot cover.

   `SubagentTool.ReadOnly()` and `ParallelTool.ReadOnly()` STAY `true`: the
   property describes a tool's general dispatch posture (read-only fan-out is
   the common case), and the merge-completing call is carved out per-call rather
   than by flipping the whole tool to mutating (which would serialize innocent
   read-only fan-out too).

**3. Merge-back fires only on a clean terminal, and is atomic-or-nothing.** The
merge runs as a post-run step only when the child reached a non-error,
non-cancelled terminal (a `StopError`/`StopCancelled` child's partial,
internally-inconsistent edits NEVER land while the model is told it failed).
The merge first runs `git apply --check`; a patch that will not apply cleanly is
rejected WITHOUT modifying the parent tree (no partial write). On conflict the
fork is PRESERVED and the model gets an actionable error (cause + fork path +
"review with Read, apply with Edit/Write, or re-delegate narrower; do NOT simply
retry").

**4. Security parity with the Parallel merge (hardening shared in the one
merger).** The merge's `git diff` runs `--no-textconv` and refuses any patch
touching `.gitattributes` (closing the `diff.<drv>.textconv` /
`filter.<drv>.smudge` RCE classes from an attacker-authored fork `.git`); the
refusal now ALSO covers an UNTRACKED `.gitattributes` created in the fork (the
untracked-copy step previously bypassed the patch-side check). The force-copy
runner drops the issue-#40 worktree-trust gate soundly: force-copy does no
fork-time `git` checkout, so the checkout RCE the gate closes cannot fire;
run-time git over the copied `.git` is hardened via `gitenv.Scrub` at
main-session parity.

**5. Parallel single-branch auto-merge STAYS (coexists).** The mechanism is kept
for back-compat, but it is no longer DOCUMENTED as the implement-and-land path —
the dev-pipeline skill and the tool Spec texts now route single-task landing to
`Subagent(mode:"read-write")` and reserve `Parallel` for genuine 2+-branch
fan-out.

## Consequences

**Easier:** the three delegation tools now read cleanly — **Subagent** =
one focused task (read-only OR, with `mode:"read-write"`, implement-and-land);
**Parallel** = fan out 2+ competing/independent branches; **Team** = coordinate
long-lived members. A delegated implementer's edits land with no manual step and
no flag. The single-task land path is the natural verb, not a degenerate
fan-out.

**Harder / committed to:** `ReadOnly()` no longer fully describes a tool's
dispatch behaviour on its own — a merge-completing call is read from the args
via `MutatesParent` and dispatched serially. The contract is now "read-only
fan-out is parallel; a call that will mutate the parent on completion is
mutate-serial." Both defenses (per-run dispatch-serial + cross-run mutex) are
load-bearing and must move together with any future merge consumer.

**Costs:** a force-copy fork per writable call (heavier than a worktree — a full
tree copy including `.git`, the same cost a Parallel branch / mutating member
already pays); the per-call `git apply --check` dry-run; a new `SubagentOption`
trio + the `SerializingMerger` adapter. The writable child is SAME-PROVIDER on
the parent's model family by construction (its own re-derived
window/compactor/counter); a per-call `model` is accepted but a v1 writable
child always runs on the single writable engine (documented residual).

**Deferred (conscious non-goals this round):** a first-class "merge landed"
event for the TUI/log (a 4th delegation-observability surface would trip the
"extract a shared `ChildActivity` value object" tripwire — the merge fact rides
the model-visible result text with a files-changed count for now); a louder
operator diagnostic on a writable merge into an UNTRUSTED workspace; a writable
per-def specialist tier (`agent` + `read-write` is rejected — named specialists
run read-only in v1).

## See also

- [ADR 0039](0039-parallel-auto-merge.md) — the superseded Parallel
  single-branch auto-merge (mechanism retained, no longer the documented
  implement path).
- [ADR 0014](0014-agent-teams.md) — the three-tier workspace isolation model
  (base-share / read-only worktree / mutating force-copy) the writable Subagent
  reuses.
- `engine/agent/subagent.go` (`subagentArgs.Mode`, `WithSubagentAutoMerge`,
  `WithWritableChildEngine`, `WithWritableChildForker`, `mergeWritableChild`,
  `MutatesParent`) — the tool wiring.
- `engine/agent/dispatch.go` (`parentMutatingCaller`, the read-batchable
  predicate) — the per-call mutate-serial seam.
- `internal/adapter/forker/forker.go` (`SerializingMerger`, `mergeForkInner`'s
  `git apply --check` + untracked-`.gitattributes` refusal) — the shared,
  hardened, serialized merger.
- `internal/app/build.go` / `catalog.go` (`buildWritableSubagentChildEngine`,
  the process-wide `autoMerger` on `catalogAssets`) — composition wiring.
- `.claude/skills/dev-pipeline/SKILL.md` — the implementer path, now routed to
  the writable Subagent.
