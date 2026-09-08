// remoteenv fork/merge: the EnvironmentForker and EnvironmentMerger the fake
// provides to prove the phase-3 fork/merge contract over a non-in-tree Kind.

package remoteenv

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// Forker is the fake's tool.EnvironmentForker. Fork creates a complete isolated
// child Environment with a FRESH opaque child id, seeded from a DEEP COPY of the
// parent namespace's file map, so the child starts from the parent's contents
// and the two then diverge independently. The child's Workspace and runner share
// the child's namespace, so the child's Bash observes the SAME namespace its
// Read/Write do — never the parent's. cleanup is a no-op (the namespace is
// in-memory; a real transport would tear down the remote workspace here).
type Forker struct {
	backend *Backend
}

// NewForker constructs the fake's EnvironmentForker over backend. The backend is
// the SAME one that minted the parent Environment, so the child namespace is
// registered alongside the parent and the two share no mutable state.
func NewForker(backend *Backend) *Forker {
	return &Forker{backend: backend}
}

// Compile-time assertion that Forker satisfies the seam.
var _ tool.EnvironmentForker = (*Forker)(nil)

// Fork creates an isolated child Environment derived from base. The child
// namespace is seeded from a deep copy of base's namespace file map. The child
// carries the SAME Kind with a fresh opaque id; the parent is never mutated.
func (f *Forker) Fork(_ context.Context, base tool.Environment, label string) (tool.Environment, func() error, string, error) {
	if base.Workspace() == nil {
		return tool.Environment{}, nil, "", errors.New("remoteenv: Fork requires a base with a non-nil workspace")
	}
	parent, ok := base.Workspace().(*workspace)
	if !ok {
		return tool.Environment{}, nil, "", fmt.Errorf("remoteenv: Fork requires a remoteenv workspace, got %T", base.Workspace())
	}
	seed, err := f.backend.copyNamespace(parent.ns.id)
	if err != nil {
		return tool.Environment{}, nil, "", err
	}
	childID := f.backend.mintID(label)
	// The fork-base snapshot: a deep copy of the parent's file map at fork time,
	// the merge anchor that lets the merger distinguish a child's legitimate
	// modification from a divergent conflict (both sides changed the same path).
	baseSnap := make(map[string][]byte, len(seed))
	for k, f := range seed {
		cp := make([]byte, len(f.data))
		copy(cp, f.data)
		baseSnap[k] = cp
	}
	childNS := &namespace{id: childID, files: seed, base: baseSnap, now: parent.ns.now}
	f.backend.mu.Lock()
	f.backend.namespaces[childID] = childNS
	f.backend.mu.Unlock()
	ws := &workspace{ns: childNS}
	runner := &runner{ns: childNS}
	child, werr := tool.NewEnvironment(session.EnvironmentRef{Kind: Kind, ID: childID, Revision: base.Ref().Revision}, ws, memledger.New(), runner)
	if werr != nil {
		return tool.Environment{}, nil, "", werr
	}
	// cleanup is a no-op for the in-memory fake; a real transport would remove the
	// remote workspace. It is non-nil and safe to call once.
	cleanup := func() error { return nil }
	return child, cleanup, "", nil
}

// Merger is the fake's tool.EnvironmentMerger. Merge applies the child's
// working-tree-vs-parent diff to the parent namespace by REF, preserving the
// child on conflict (the contract: a failed merge is recoverable). It computes
// the child-only and child-modified paths against the parent and applies them;
// a path that exists in both with DIFFERENT content is a conflict — the parent
// keeps its content, the conflict path is reported, and the child is left
// intact.
type Merger struct {
	backend *Backend
}

// NewMerger constructs the fake's EnvironmentMerger over backend.
func NewMerger(backend *Backend) *Merger { return &Merger{backend: backend} }

// Compile-time assertion that Merger satisfies the seam.
var _ tool.EnvironmentMerger = (*Merger)(nil)

// Merge applies the diff of the fork at child into the parent Environment's
// namespace. It reads both namespaces by ref, computes the changes (new +
// modified files in the child vs the parent), and applies them to the parent.
// On conflict (a path present in both with different content) it returns a
// non-nil error naming the conflicts and leaves the child intact.
//
// Concurrent merges are serialized through a process-wide mutex so two merges
// into the same parent never interleave their read-compute-apply sequences
// (mirrors the forker's SerializingMerger).
func (m *Merger) Merge(_ context.Context, child, parent tool.Environment) error {
	mergeMu.Lock()
	defer mergeMu.Unlock()
	if child.Workspace() == nil {
		return errors.New("remoteenv: merge requires a child Environment with a non-nil workspace")
	}
	if parent.Workspace() == nil {
		return errors.New("remoteenv: merge requires a parent Environment with a non-nil workspace")
	}
	childWS, ok := child.Workspace().(*workspace)
	if !ok {
		return fmt.Errorf("remoteenv: merge requires a remoteenv child workspace, got %T", child.Workspace())
	}
	parentWS, ok := parent.Workspace().(*workspace)
	if !ok {
		return fmt.Errorf("remoteenv: merge requires a remoteenv parent workspace, got %T", parent.Workspace())
	}
	// Guard: the refs must name namespaces the backend knows (the contract is
	// ref-addressed, not path-addressed).
	if _, err := m.backend.namespace(child.Ref().ID); err != nil {
		return err
	}
	if _, err := m.backend.namespace(parent.Ref().ID); err != nil {
		return err
	}

	childFiles := childWS.namespaceFiles()
	parentFiles := parentWS.namespaceFiles()
	base := childWS.namespaceBase()

	// Compute the child's changes vs the fork-base (the merge anchor): a path is
	// a change if it is NEW (absent from the base), or its content differs from
	// the base. Deletions are out of scope for this contract proof (a file
	// present in base but absent in the child is not a deletion the merge
	// applies; the fake's merge is additive, like the forker's git-diff-HEAD
	// approach for untracked files).
	changes := make(map[string][]byte)
	for k, d := range childFiles {
		if b, ok := base[k]; ok && versionOf(b).Equal(versionOf(d)) {
			continue // unchanged from fork-base
		}
		changes[k] = d
	}
	if len(changes) == 0 {
		return nil // clean child — nothing to merge
	}

	// Detect TRUE divergent conflicts: a path the child changed AND the parent
	// also changed (vs the base). A child change to a path the parent did NOT
	// touch (parent == base) is a clean landing. base == nil means the child was
	// not forked through this fake's Forker — fall back to the parent-only
	// conflict rule (any existing differing path conflicts) so a hand-built
	// child still fails honestly rather than silently overwriting.
	if base != nil {
		var conflicts []string
		clean := make(map[string][]byte, len(changes))
		for k, d := range changes {
			p, pOK := parentFiles[k]
			bv, bOK := base[k]
			if pOK && bOK && !versionOf(p).Equal(versionOf(bv)) {
				// Both sides diverged from the base — true conflict.
				conflicts = append(conflicts, k)
				continue
			}
			clean[k] = d
		}
		if len(clean) > 0 {
			_ = parentWS.forceApplyFiles(clean)
		}
		if len(conflicts) > 0 {
			sort.Strings(conflicts)
			return fmt.Errorf("remoteenv: merge of child %q into parent %q conflicted on %d path(s) (the child is preserved at %q for manual resolution): %s",
				child.Ref().ID, parent.Ref().ID, len(conflicts), child.Ref().ID, joinPaths(conflicts))
		}
		return nil
	}

	conflicts := parentWS.applyFiles(changes)
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return fmt.Errorf("remoteenv: merge of child %q into parent %q conflicted on %d path(s) (the child is preserved at %q for manual resolution): %s",
			child.Ref().ID, parent.Ref().ID, len(conflicts), child.Ref().ID, joinPaths(conflicts))
	}
	return nil
}

// joinPaths renders a short, comma-separated path list for a conflict error.
func joinPaths(ps []string) string {
	const maxPaths = 8
	if len(ps) > maxPaths {
		return fmt.Sprintf("%s ... (%d more)", joinN(ps[:maxPaths]), len(ps)-maxPaths)
	}
	return joinN(ps)
}

func joinN(ps []string) string {
	var b []byte
	for i, p := range ps {
		if i > 0 {
			b = append(b, ',', ' ')
		}
		b = append(b, p...)
	}
	return string(b)
}

// mergeMu is a process-wide serializer so concurrent merges never interleave
// their read-compute-apply sequences into a parent namespace (mirrors the
// forker's SerializingMerger). The fake's namespace mutex already serializes
// per-namespace writes, but the merge's read-compute-apply is not atomic
// across namespaces; this gate keeps the sequence globally ordered for the
// contract proof.
var mergeMu sync.Mutex
