package tool

import "context"

// WorkspaceForker is the workspace-isolation seam for fork-join parallelism
// (harness pattern 8). It produces an isolated CHILD Workspace derived from a
// base Workspace so a forked agent loop can read — and, when its catalog allows
// it, WRITE — without racing on, or mutating, the shared base tree.
//
// It lives here in engine/tool, next to Workspace, for the same layering
// reason Workspace does: port already imports tool, so a separate package would
// risk the port↔tool import cycle. The interface is additive — no frozen domain
// type changes.
//
// Implementations may isolate via a git worktree, a recursive directory copy, or
// an overlay; the agent never knows (or cares) which. The contract is only:
//
//   - the returned child is a fully usable Workspace rooted at an isolated path;
//   - writes through the child do NOT affect the base tree;
//   - cleanup tears the child down (removes the worktree/copy) and is safe to
//     call exactly once after the child is no longer in use.
type WorkspaceForker interface {
	// Fork creates an isolated child workspace derived from base, returning the
	// child workspace, a cleanup func, and an OPTIONAL degraded-fork advisory. label
	// is a short, human-meaningful tag (e.g. the branch idea or task name)
	// implementations MAY fold into the child path or worktree branch for
	// observability; it need not be unique and is not load-bearing. Implementations
	// may use git worktrees, a copy, or an overlay; the agent never knows which. The
	// returned cleanup is always non-nil when err is nil and removes the child's
	// backing storage.
	//
	// advisory is an OPTIONAL human-readable note about a DEGRADED-but-usable fork —
	// empty in the normal case. It is the channel for "the child is usable, but not
	// exactly the workspace you'd expect": an implementation that could not fully
	// reproduce the base (e.g. an overlay that fell back to a clean checkout) returns
	// a note describing what the child is actually seeing, and the agent MAY prepend
	// it to the child's view so the child reasons honestly about the degradation
	// rather than silently. It is generic (not tied to any one isolation strategy)
	// and never load-bearing for safety — purely informational.
	Fork(ctx context.Context, base Workspace, label string) (child Workspace, cleanup func() error, advisory string, err error)
}

// ForkMerger is the OPTIONAL seam by which a preserved fork's changes are merged
// BACK into the parent workspace. It serves TWO delegation paths: Parallel's
// single-branch fast path (a Parallel run with exactly one branch and
// join=first/judge applies the winner's diff to the parent) AND the writable
// Subagent (mode:"read-write" — a force-copy explorer's edits are merged back), so
// a delegated implementer's edits actually LAND without a manual copy/merge step.
//
// It lives here in engine/tool, next to WorkspaceForker, for the same layering
// reason (port already imports tool). The interface is additive — no frozen
// domain type changes.
//
// Contract:
//   - Merge applies the fork's working-tree-vs-HEAD diff to the parent
//     workspace. It MUST NOT force: on a conflict it returns a non-nil error
//     naming the conflict and the fork path so the operator can resolve
//     manually. The fork is left intact (the caller still owns its cleanup /
//     reaper slot) so a failed merge is recoverable.
//   - The merge runs in the PARENT workspace under the PARENT's trust posture,
//     not the fork's — the fork's content is untrusted child-authored data, but
//     applying a diff is a parent-side operation (the same trust the parent's
//     own Edit/Write carries). Composition decides whether to wire a merger at
//     all — when wired, auto-merge is DEFAULT-ON (no flag; see ADR 0039). The
//     composition-injected merger is SERIALIZED process-wide (a single mutex in a
//     serializing decorator) so concurrent merges from Parallel and the writable
//     Subagent never interleave their writes into a parent workspace.
//   - nil merger (the default) means no auto-merge: the historical no-auto-merge
//     boundary holds unchanged. ParallelTool.ReadOnly()/SubagentTool.ReadOnly()
//     stay true so read-only fan-out keeps batching in parallel; but a CALL that
//     will actually merge is excluded from the concurrent read batch via
//     MutatesParent (dispatch-serial, flushed alone — see agent.parentMutatingCaller),
//     so its post-run merge never overlaps a sibling parent read, and cross-run
//     merge-vs-merge is serialized by the shared SerializingMerger mutex.
type ForkMerger interface {
	// Merge applies the diff of the fork at forkRoot (its working tree vs its
	// HEAD) into the parent workspace parentWS. On conflict it returns a
	// non-nil error describing the conflict; the fork at forkRoot is left in
	// place for manual resolution.
	Merge(ctx context.Context, forkRoot string, parentWS Workspace) error
}
