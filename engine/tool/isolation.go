package tool

import "context"

// EnvironmentForker is the environment-isolation seam for fork-join parallelism
// (harness pattern 8, ADR 0105). It produces an isolated CHILD Environment (Workspace +
// command runner bound to the child namespace + ref) derived from a base
// Environment so a forked agent loop can read — and, when its catalog allows
// it, WRITE — without racing on, or mutating, the shared base tree.
//
// It replaces the former WorkspaceForker (issue #462). The forker now returns a
// COMPLETE child Environment — Workspace AND a command runner bound to the
// child namespace — so a forked child's Bash observes the SAME child namespace
// its Read/Write do, never the parent's. The runner is bound by the forker
// (the forker owns the git-worktree / force-copy isolation), so the per-call
// workdir the old seam threaded is gone.
//
// It lives here in engine/tool, next to Environment, for the same layering
// reason Environment does: port already imports tool, so a separate package
// would risk the port↔tool import cycle. The interface replaces the old
// WorkspaceForker in this intentional breaking change (issue #462); the two
// seams are NOT maintained in parallel.
//
// Implementations may isolate via a git worktree, a recursive directory copy,
// or an overlay; the agent never knows (or cares) which. The contract is only:
//
//   - the returned child is a fully usable Environment rooted at an isolated
//     namespace (Workspace + bound runner);
//   - writes through the child do NOT affect the base tree;
//   - cleanup tears the child down (removes the worktree/copy) and is safe to
//     call exactly once after the child is no longer in use.
type EnvironmentForker interface {
	// Fork creates an isolated child Environment derived from base, returning
	// the child Environment, a cleanup func, and an OPTIONAL degraded-fork
	// advisory. label is a short, human-meaningful tag (e.g. the branch idea or
	// task name) implementations MAY fold into the child path or worktree
	// branch for observability; it need not be unique and is not load-bearing.
	// Implementations may use git worktrees, a copy, or an overlay; the agent
	// never knows which. The returned cleanup is always non-nil when err is nil
	// and removes the child's backing storage.
	//
	// advisory is an OPTIONAL human-readable note about a DEGRADED-but-usable
	// fork — empty in the normal case. It is the channel for "the child is
	// usable, but not exactly the workspace you'd expect": an implementation
	// that could not fully reproduce the base (e.g. an overlay that fell back to
	// a clean checkout) returns a note describing what the child is actually
	// seeing, and the agent MAY prepend it to the child's view so the child
	// reasons honestly about the degradation rather than silently. It is
	// generic (not tied to any one isolation strategy) and never load-bearing
	// for safety — purely informational.
	Fork(ctx context.Context, base Environment, label string) (child Environment, cleanup func() error, advisory string, err error)
}

// EnvironmentMerger is the OPTIONAL seam by which a preserved fork's changes
// are merged BACK into the parent Environment. It serves ONE delegation path:
// Parallel's single-branch fast path (a Parallel run with exactly one branch
// and join=first/judge applies the winner's diff to the parent), so a
// delegated implementer's edits actually LAND without a manual copy/merge
// step. (The writable Subagent does NOT use this seam — mode:"read-write"
// edits the parent tree directly during the run; see ADR 0041.)
//
// It replaces the former ForkMerger (issue #462). Merge now receives the CHILD
// and PARENT Environments (not a forkRoot string + parent Workspace): the
// merger reads the child's identity and workspace off the child Environment
// and applies its diff into the parent Environment's workspace. No forkRoot
// string crosses the core interface.
//
// It lives here in engine/tool, next to EnvironmentForker, for the same
// layering reason (port already imports tool). The interface replaces the old
// ForkMerger in this intentional breaking change (issue #462); the two seams
// are NOT maintained in parallel.
//
// Contract:
//   - Merge applies the fork's working-tree-vs-HEAD diff to the parent
//     workspace. It MUST NOT force: on a conflict it returns a non-nil error
//     naming the conflict so the operator can resolve manually. The fork is
//     left intact (the caller still owns its cleanup / reaper slot) so a
//     failed merge is recoverable.
//   - The merge runs in the PARENT workspace under the PARENT's trust posture,
//     not the fork's — the fork's content is untrusted child-authored data,
//     but applying a diff is a parent-side operation (the same trust the
//     parent's own Edit/Write carries). Composition decides whether to wire a
//     merger at all — when wired, auto-merge is DEFAULT-ON (no flag; see ADR
//     0039). The composition-injected merger is SERIALIZED process-wide (a
//     single mutex in a serializing decorator) so concurrent merges from
//     Parallel never interleave their writes into a parent workspace.
//   - nil merger (the default) means no auto-merge: the historical no-auto-merge
//     boundary holds unchanged. ParallelTool.ReadOnly()/SubagentTool.ReadOnly()
//     stay true so read-only fan-out keeps batching in parallel; but a CALL
//     that will actually merge is excluded from the concurrent read batch via
//     MutatesParent (dispatch-serial, flushed alone — see agent.parentMutatingCaller),
//     so its post-run merge never overlaps a sibling parent read, and
//     cross-run merge-vs-merge is serialized by the shared SerializingMerger
//     mutex.
type EnvironmentMerger interface {
	// Merge applies the diff of the fork at child (its working tree vs its
	// HEAD) into the parent Environment's workspace. On conflict it returns a
	// non-nil error describing the conflict; the fork (the child Environment)
	// is left in place for manual resolution.
	Merge(ctx context.Context, child, parent Environment) error
}
