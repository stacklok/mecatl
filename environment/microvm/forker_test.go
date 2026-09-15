package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/gitexec"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

func TestMicroVMEnvironments_Scenario7_IsolatedChildrenUseCompleteEnvironments(t *testing.T) {
	t.Parallel()
	parentWS := memfs.NewWorkspace("/workspace")
	parent := tool.MustEnvironment(session.EnvironmentRef{Kind: Kind, ID: "parent@1"}, parentWS, memledger.New(), namespaceRunner{namespace: "parent"})
	driver := newForkDriver(parent.Ref())
	forker := NewEnvironmentForker(driver, 4)

	for _, label := range []string{"subagent", "parallel", "team"} {
		child, cleanup, _, err := forker.Fork(context.Background(), parent, label)
		if err != nil {
			t.Fatalf("Fork(%q): %v", label, err)
		}
		if child.Workspace() == nil || child.CommandRunner() == nil {
			t.Fatalf("Fork(%q) returned incomplete environment", label)
		}
		if child.Ref() == parent.Ref() || child.Workspace().Root() == parent.Workspace().Root() {
			t.Fatalf("Fork(%q) reused parent namespace: ref=%+v root=%q", label, child.Ref(), child.Workspace().Root())
		}
		result, err := child.CommandRunner().Run(context.Background(), "pwd")
		if err != nil || result.Stdout != child.Workspace().Root() {
			t.Fatalf("Fork(%q) workspace/runner affinity: stdout=%q err=%v root=%q", label, result.Stdout, err, child.Workspace().Root())
		}
		if _, err := child.Workspace().CreateFile(context.Background(), "child.txt", []byte(label)); err != nil {
			t.Fatalf("child CreateFile: %v", err)
		}
		if _, err := parentWS.Read(context.Background(), "child.txt"); err == nil {
			t.Fatalf("Fork(%q) mutated parent before merge", label)
		}
		if err := cleanup(); err != nil {
			t.Fatalf("cleanup(%q): %v", label, err)
		}
	}
}

func TestMicroVMEnvironments_Scenario7_ConcurrentChildrenAreIsolatedAndBounded(t *testing.T) {
	t.Parallel()
	parent := tool.MustEnvironment(session.EnvironmentRef{Kind: Kind, ID: "parent@1"}, memfs.NewWorkspace("/workspace"), memledger.New(), namespaceRunner{namespace: "parent"})
	driver := newForkDriver(parent.Ref())
	driver.block = make(chan struct{})
	forker := NewEnvironmentForker(driver, 2)

	type result struct {
		child   tool.Environment
		cleanup func() error
		err     error
	}
	results := make(chan result, 3)
	for i := 0; i < 2; i++ {
		go func(i int) {
			child, cleanup, _, err := forker.Fork(context.Background(), parent, fmt.Sprintf("branch-%d", i))
			results <- result{child: child, cleanup: cleanup, err: err}
		}(i)
	}
	driver.waitStarted(t, 2)

	_, _, _, err := forker.Fork(context.Background(), parent, "over-quota")
	if !errors.Is(err, ErrForkQuotaExceeded) {
		t.Fatalf("third concurrent Fork error = %v, want ErrForkQuotaExceeded", err)
	}
	close(driver.block)

	var children []tool.Environment
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatalf("admitted Fork: %v", got.err)
		}
		children = append(children, got.child)
		defer func(cleanup func() error) {
			if err := cleanup(); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}(got.cleanup)
	}
	if children[0].Ref() == children[1].Ref() || children[0].Workspace().Root() == children[1].Workspace().Root() {
		t.Fatalf("concurrent children shared ref or worktree: (%+v, %q) (%+v, %q)", children[0].Ref(), children[0].Workspace().Root(), children[1].Ref(), children[1].Workspace().Root())
	}

	forks := driver.snapshot()
	if len(forks) != 2 || forks[0].MetadataPath == forks[1].MetadataPath || forks[0].Endpoint == forks[1].Endpoint || forks[0].Generation == forks[1].Generation {
		t.Fatalf("concurrent child identities are not isolated: %+v", forks)
	}
}

type namespaceRunner struct{ namespace string }

func (r namespaceRunner) Run(context.Context, string) (tool.CommandResult, error) {
	return tool.CommandResult{Stdout: r.namespace}, nil
}

type recordedFork struct {
	MetadataPath string
	Endpoint     string
	Generation   uint32
}

type forkDriver struct {
	mu      sync.Mutex
	parent  session.EnvironmentRef
	seq     uint32
	started chan struct{}
	block   chan struct{}
	forks   []recordedFork
}

func newForkDriver(parent session.EnvironmentRef) *forkDriver {
	return &forkDriver{parent: parent, started: make(chan struct{}, 16)}
}

func (d *forkDriver) Fork(ctx context.Context, request ForkRequest) (ForkedEnvironment, error) {
	if request.Parent != d.parent {
		return ForkedEnvironment{}, fmt.Errorf("fork parent = %+v, want %+v", request.Parent, d.parent)
	}
	d.mu.Lock()
	d.seq++
	seq := d.seq
	d.mu.Unlock()
	d.started <- struct{}{}
	if d.block != nil {
		select {
		case <-d.block:
		case <-ctx.Done():
			return ForkedEnvironment{}, context.Cause(ctx)
		}
	}
	root := fmt.Sprintf("/child/%d", seq)
	ref := session.EnvironmentRef{Kind: Kind, ID: fmt.Sprintf("child-%d@%d", seq, seq+1)}
	fork := recordedFork{MetadataPath: fmt.Sprintf("/metadata/%d", seq), Endpoint: fmt.Sprintf("endpoint-%d", seq), Generation: seq + 1}
	d.mu.Lock()
	d.forks = append(d.forks, fork)
	d.mu.Unlock()
	return ForkedEnvironment{
		Environment:  tool.MustEnvironment(ref, memfs.NewWorkspace(root), memledger.New(), namespaceRunner{namespace: root}),
		WorktreePath: root,
		MetadataPath: fork.MetadataPath,
		Endpoint:     fork.Endpoint,
		Generation:   fork.Generation,
		BaseRevision: fmt.Sprintf("base-%d", seq),
	}, nil
}

func (*forkDriver) Merge(context.Context, MergeRequest) error { return nil }

func (*forkDriver) Destroy(context.Context, session.EnvironmentRef) error { return nil }

func (d *forkDriver) waitStarted(t *testing.T, count int) {
	t.Helper()
	for range count {
		select {
		case <-d.started:
		case <-t.Context().Done():
			t.Fatal("timed out waiting for driver fork")
		}
	}
}

func (d *forkDriver) snapshot() []recordedFork {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]recordedFork(nil), d.forks...)
}

type mergeForkDriver struct {
	mu                    sync.Mutex
	parentRef             session.EnvironmentRef
	parent                map[string]string
	children              map[session.EnvironmentRef]map[string]string
	bases                 map[session.EnvironmentRef]map[string]string
	baseRevisions         map[session.EnvironmentRef]string
	destroyedRefs         map[session.EnvironmentRef]bool
	seq                   uint32
	mergeStarted          chan struct{}
	blockMerges           chan struct{}
	concurrentMergeCount  int
	maxConcurrentMergeCnt int
	destroyCtxCancelled   bool
}

func newMergeForkDriver(parent session.EnvironmentRef, files map[string]string) *mergeForkDriver {
	return &mergeForkDriver{
		parentRef: parent, parent: maps.Clone(files), children: make(map[session.EnvironmentRef]map[string]string),
		bases: make(map[session.EnvironmentRef]map[string]string), baseRevisions: make(map[session.EnvironmentRef]string),
		destroyedRefs: make(map[session.EnvironmentRef]bool), mergeStarted: make(chan struct{}, 8),
	}
}

func (d *mergeForkDriver) Fork(_ context.Context, request ForkRequest) (ForkedEnvironment, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if request.Parent != d.parentRef {
		return ForkedEnvironment{}, ErrInvalidFork
	}
	d.seq++
	generation := d.seq + 1
	ref := session.EnvironmentRef{Kind: Kind, ID: fmt.Sprintf("merge-child-%d@%d", d.seq, generation)}
	base := maps.Clone(d.parent)
	baseRevision := fmt.Sprintf("base-%d", d.seq)
	d.bases[ref] = base
	d.children[ref] = maps.Clone(base)
	d.baseRevisions[ref] = baseRevision
	root := fmt.Sprintf("/merge-child/%d", d.seq)
	return ForkedEnvironment{
		Environment:  tool.MustEnvironment(ref, memfs.NewWorkspace(root), memledger.New(), namespaceRunner{namespace: root}),
		WorktreePath: root, MetadataPath: root + "/git", Endpoint: fmt.Sprintf("merge-endpoint-%d", d.seq),
		Generation: generation, BaseRevision: baseRevision,
	}, nil
}

func (d *mergeForkDriver) Merge(ctx context.Context, request MergeRequest) error {
	d.mu.Lock()
	d.concurrentMergeCount++
	if d.concurrentMergeCount > d.maxConcurrentMergeCnt {
		d.maxConcurrentMergeCnt = d.concurrentMergeCount
	}
	block := d.blockMerges
	d.mu.Unlock()
	d.mergeStarted <- struct{}{}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			d.finishMerge()
			return context.Cause(ctx)
		}
	}
	defer d.finishMerge()

	d.mu.Lock()
	defer d.mu.Unlock()
	base, exists := d.bases[request.Child]
	child := d.children[request.Child]
	if !exists || request.Parent != d.parentRef || (request.BaseRevision != "" && request.BaseRevision != d.baseRevisions[request.Child]) {
		return ErrInvalidFork
	}
	changed := make(map[string]struct{})
	for path, baseValue := range base {
		if childValue, ok := child[path]; !ok || childValue != baseValue {
			changed[path] = struct{}{}
		}
	}
	for path, childValue := range child {
		if baseValue, ok := base[path]; !ok || childValue != baseValue {
			changed[path] = struct{}{}
		}
	}
	for path := range changed {
		baseValue, baseOK := base[path]
		parentValue, parentOK := d.parent[path]
		if baseOK != parentOK || baseValue != parentValue {
			return fmt.Errorf("%w: %s", ErrMergeConflict, path)
		}
	}
	next := maps.Clone(d.parent)
	for path := range changed {
		if value, ok := child[path]; ok {
			next[path] = value
		} else {
			delete(next, path)
		}
	}
	d.parent = next
	return nil
}

func (d *mergeForkDriver) finishMerge() {
	d.mu.Lock()
	d.concurrentMergeCount--
	d.mu.Unlock()
}

func (d *mergeForkDriver) Destroy(ctx context.Context, ref session.EnvironmentRef) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.destroyCtxCancelled = d.destroyCtxCancelled || ctx.Err() != nil
	d.destroyedRefs[ref] = true
	delete(d.children, ref)
	return nil
}

func (d *mergeForkDriver) setChild(ref session.EnvironmentRef, path, value string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.children[ref][path] = value
}

func (d *mergeForkDriver) deleteChild(ref session.EnvironmentRef, path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.children[ref], path)
}

func (d *mergeForkDriver) setParent(path, value string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.parent[path] = value
}

func (d *mergeForkDriver) parentSnapshot() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return maps.Clone(d.parent)
}

func (d *mergeForkDriver) inspectable(ref session.EnvironmentRef) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.children[ref]
	return ok
}

func (d *mergeForkDriver) destroyed(ref session.EnvironmentRef) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.destroyedRefs[ref]
}

func (d *mergeForkDriver) destroyContextCancelled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.destroyCtxCancelled
}

func (d *mergeForkDriver) waitMergeStarted(t *testing.T) {
	t.Helper()
	select {
	case <-d.mergeStarted:
	case <-t.Context().Done():
		t.Fatal("timed out waiting for merge")
	}
}

func (d *mergeForkDriver) maxConcurrentMerges() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.maxConcurrentMergeCnt
}

type daemonMergeRaceChildren struct {
	mu        sync.Mutex
	entered   chan struct{}
	release   chan struct{}
	version   int
	active    int
	maxActive int
}

func newDaemonMergeRaceChildren() *daemonMergeRaceChildren {
	return &daemonMergeRaceChildren{entered: make(chan struct{}, 2), release: make(chan struct{})}
}

func (*daemonMergeRaceChildren) Fork(context.Context, EnvironmentRecord, string) (EnvironmentRecord, error) {
	return EnvironmentRecord{}, ErrInvalidFork
}

func (c *daemonMergeRaceChildren) Merge(ctx context.Context, _, _ EnvironmentRecord) error {
	c.mu.Lock()
	base := 0
	c.active++
	if c.active > c.maxActive {
		c.maxActive = c.active
	}
	c.mu.Unlock()
	c.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-c.release:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active--
	if c.version != base {
		return ErrMergeConflict
	}
	c.version++
	return nil
}

func (c *daemonMergeRaceChildren) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-c.entered:
	case <-time.After(time.Second):
		t.Fatal("merge did not enter child transaction")
	}
}

func (c *daemonMergeRaceChildren) maxConcurrent() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxActive
}

func TestMicroVMEnvironments_Scenario7_MergeIsConflictAwareAndPreservesChild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("daemon serializes clients for one parent", func(t *testing.T) {
		parentRecord := readyRecord("daemon-parent", 1)
		firstRecord := readyRecord("daemon-child-first", 2)
		firstRecord.ParentRef, firstRecord.ForkBase = parentRecord.Ref, "base"
		secondRecord := readyRecord("daemon-child-second", 3)
		secondRecord.ParentRef, secondRecord.ForkBase = parentRecord.Ref, "base"
		registry := newLifecycleRegistry(parentRecord, firstRecord, secondRecord)
		children := newDaemonMergeRaceChildren()
		daemon := &Daemon{registry: registry, children: children}
		request := func(child EnvironmentRecord) LifecycleRequest {
			payload, err := json.Marshal(ChildMergePayload{Child: bindingForRecord(child)})
			if err != nil {
				t.Fatal(err)
			}
			return LifecycleRequest{Binding: bindingForRecord(parentRecord), Payload: payload}
		}
		results := make(chan error, 2)
		go func() { _, err := daemon.merge(ctx, request(firstRecord)); results <- err }()
		children.waitEntered(t)
		go func() { _, err := daemon.merge(ctx, request(secondRecord)); results <- err }()
		select {
		case <-children.entered:
			t.Fatal("second client entered parent merge transaction before first apply completed")
		case <-time.After(50 * time.Millisecond):
		}
		close(children.release)
		var applied, conflicted int
		for range 2 {
			err := <-results
			switch {
			case err == nil:
				applied++
			case errors.Is(err, ErrMergeConflict):
				conflicted++
			default:
				t.Fatalf("daemon merge error = %v", err)
			}
		}
		if applied != 1 || conflicted != 1 || children.maxConcurrent() != 1 {
			t.Fatalf("daemon merge outcomes applied=%d conflicted=%d max-concurrent=%d", applied, conflicted, children.maxConcurrent())
		}
	})

	parent := tool.MustEnvironment(session.EnvironmentRef{Kind: Kind, ID: "parent@1"}, memfs.NewWorkspace("/workspace"), memledger.New(), namespaceRunner{namespace: "parent"})
	driver := newMergeForkDriver(parent.Ref(), map[string]string{
		"replace.txt": "base replacement",
		"delete.txt":  "base deletion",
		"stable.txt":  "unchanged",
	})
	forker := NewEnvironmentForker(driver, 4)

	child, cleanup, _, err := forker.Fork(ctx, parent, "merge-all-kinds")
	if err != nil {
		t.Fatal(err)
	}
	driver.setChild(child.Ref(), "added.txt", "addition")
	driver.setChild(child.Ref(), "replace.txt", "replacement")
	driver.deleteChild(child.Ref(), "delete.txt")
	if err := forker.Merge(ctx, child, parent); err != nil {
		t.Fatalf("Merge() additions/replacements/deletions: %v", err)
	}
	if got := driver.parentSnapshot(); !maps.Equal(got, map[string]string{
		"added.txt":   "addition",
		"replace.txt": "replacement",
		"stable.txt":  "unchanged",
	}) {
		t.Fatalf("merged parent = %#v", got)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}

	conflicting, conflictingCleanup, _, err := forker.Fork(ctx, parent, "conflict")
	if err != nil {
		t.Fatal(err)
	}
	driver.setChild(conflicting.Ref(), "replace.txt", "child value")
	driver.setChild(conflicting.Ref(), "uncertain.txt", "must not partially apply")
	driver.setParent("replace.txt", "concurrent parent value")
	before := driver.parentSnapshot()
	err = forker.Merge(ctx, conflicting, parent)
	if !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("Merge() error = %v, want ErrMergeConflict", err)
	}
	if got := driver.parentSnapshot(); !maps.Equal(got, before) {
		t.Fatalf("conflicting merge partially changed parent: before=%#v after=%#v", before, got)
	}
	if !driver.inspectable(conflicting.Ref()) || driver.destroyed(conflicting.Ref()) {
		t.Fatal("conflicting child was not preserved for inspection")
	}
	if err := conflictingCleanup(); err != nil {
		t.Fatal(err)
	}

	first, firstCleanup, _, err := forker.Fork(ctx, parent, "serialized-1")
	if err != nil {
		t.Fatal(err)
	}
	second, secondCleanup, _, err := forker.Fork(ctx, parent, "serialized-2")
	if err != nil {
		t.Fatal(err)
	}
	driver.blockMerges = make(chan struct{})
	driver.mergeStarted = make(chan struct{}, 8)
	results := make(chan error, 2)
	go func() { results <- forker.Merge(ctx, first, parent) }()
	driver.waitMergeStarted(t)
	go func() { results <- forker.Merge(ctx, second, parent) }()
	select {
	case <-driver.mergeStarted:
		t.Fatal("second merge entered driver before the first parent merge completed")
	case <-time.After(50 * time.Millisecond):
	}
	if got := driver.maxConcurrentMerges(); got != 1 {
		t.Fatalf("concurrent merges for one parent = %d, want 1", got)
	}
	close(driver.blockMerges)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("serialized Merge(): %v", err)
		}
	}
	_ = firstCleanup()
	_ = secondCleanup()

	// A fork base is the parent's complete dirty tree, not merely committed HEAD.
	// Later parent changes cannot alter the child snapshot, and inherited dirty
	// paths are not mistaken for child edits during merge.
	dirtyRoot := t.TempDir()
	dirtySource := initScenario6Repository(t, dirtyRoot)
	if err := os.WriteFile(filepath.Join(dirtySource, "tracked.txt"), []byte("dirty parent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirtySource, "untracked.txt"), []byte("untracked parent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirtyBase, err := captureForkBase(ctx, dirtySource)
	if err != nil {
		t.Fatalf("captureForkBase(): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirtySource, "tracked.txt"), []byte("later parent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirtyChild, err := worktree.New().Prepare(ctx, worktree.Request{
		Source: dirtySource, WorktreePath: filepath.Join(dirtyRoot, "dirty-child"), MetadataPath: filepath.Join(dirtyRoot, "dirty-metadata"), Branch: "mecatl/dirty-child", BaseRevision: dirtyBase,
	})
	if err != nil {
		t.Fatalf("prepare dirty-base child: %v", err)
	}
	defer func() { _ = dirtyChild.Cleanup(context.Background()) }()
	if got := string(mustRead(t, filepath.Join(dirtyChild.WorktreePath, "tracked.txt"))); got != "dirty parent\n" {
		t.Fatalf("child tracked snapshot = %q, want exact fork-time state", got)
	}
	if got := string(mustRead(t, filepath.Join(dirtyChild.WorktreePath, "untracked.txt"))); got != "untracked parent\n" {
		t.Fatalf("child untracked snapshot = %q, want exact fork-time state", got)
	}
	if err := os.WriteFile(filepath.Join(dirtyChild.WorktreePath, "child-only.txt"), []byte("child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirtyParentRecord := readyRecord("dirty-parent", 1)
	dirtyParentRecord.WorktreePath = dirtySource
	dirtyChildRecord := readyRecord("dirty-child", 2)
	dirtyChildRecord.WorktreePath = dirtyChild.WorktreePath
	dirtyChildRecord.ParentRef = dirtyParentRecord.Ref
	dirtyChildRecord.ForkBase = dirtyBase
	if err := NewLifecycleChildren(nil, nil, nil, nil).Merge(ctx, dirtyParentRecord, dirtyChildRecord); err != nil {
		t.Fatalf("merge exact dirty-base child: %v", err)
	}
	if got := string(mustRead(t, filepath.Join(dirtySource, "tracked.txt"))); got != "later parent\n" {
		t.Fatalf("merge overwrote unrelated post-fork parent state: %q", got)
	}

	// Exercise the production Git merge implementation, not only the seam fake.
	root := t.TempDir()
	source := initScenario6Repository(t, root)
	baseBytes, err := gitexec.Run(ctx, source, nil, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := worktree.New().Prepare(ctx, worktree.Request{
		Source: source, WorktreePath: filepath.Join(root, "merge-child"), MetadataPath: filepath.Join(root, "merge-metadata"), Branch: "mecatl/merge-child",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = prepared.Cleanup(context.Background()) }()
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "addition.txt"), []byte("child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parentRecord := readyRecord("real-parent", 1)
	parentRecord.Ref = EnvironmentRef{Kind: Kind, ID: "real-parent@1"}
	parentRecord.WorktreePath = source
	childRecord := readyRecord("real-child", 2)
	childRecord.Ref = EnvironmentRef{Kind: Kind, ID: "real-child@2"}
	childRecord.WorktreePath = prepared.WorktreePath
	childRecord.ParentRef = parentRecord.Ref
	childRecord.ForkBase = strings.TrimSpace(string(baseBytes))
	production := NewLifecycleChildren(nil, nil, nil, nil)
	if err := production.Merge(ctx, parentRecord, childRecord); err != nil {
		t.Fatalf("production Merge(): %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(source, "addition.txt")); err != nil || string(data) != "child\n" {
		t.Fatalf("production merge addition=%q err=%v", data, err)
	}
	if err := os.WriteFile(filepath.Join(source, "addition.txt"), []byte("parent conflict\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "addition.txt"), []byte("child conflict\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := production.Merge(ctx, parentRecord, childRecord); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("production conflicting Merge() error = %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(prepared.WorktreePath, "addition.txt")); err != nil || string(data) != "child conflict\n" {
		t.Fatalf("conflict did not preserve child: data=%q err=%v", data, err)
	}
}

func TestMicroVMEnvironments_Scenario7_ChildLifecycleSurvivesTerminationPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	registry, err := OpenFileRegistry(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	parent := readyRecord("parent-life", 1)
	child := readyRecord("child-life", 2)
	child.SessionID = "parallel-parent-life-1"
	child.ParentRef = parent.Ref
	child.ForkBase = "immutable-base-1"
	if err := registry.Save(ctx, parent); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(ctx, child); err != nil {
		t.Fatal(err)
	}

	// Reopening models daemon/harness termination: exact child parentage and base
	// survive, so resume cannot attach it to a sibling parent.
	restarted, err := OpenFileRegistry(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Lookup(ctx, child.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentRef != parent.Ref || got.ForkBase != child.ForkBase {
		t.Fatalf("restart lost child identity: %+v", got)
	}
	changedBase := got
	changedBase.ForkBase = "rewritten-base"
	if err := restarted.Save(ctx, changedBase); !errors.Is(err, ErrEnvironmentStale) {
		t.Fatalf("rewriting immutable fork base error = %v, want ErrEnvironmentStale", err)
	}
	changedParent := got
	changedParent.ParentRef = readyRecord("sibling-parent", 9).Ref
	if err := restarted.Save(ctx, changedParent); !errors.Is(err, ErrEnvironmentStale) {
		t.Fatalf("rewriting child parent error = %v, want ErrEnvironmentStale", err)
	}

	parentEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: Kind, ID: "resume-parent@1"}, memfs.NewWorkspace("/workspace"), memledger.New(), namespaceRunner{namespace: "parent"})
	resumeDriver := newMergeForkDriver(parentEnv.Ref(), map[string]string{"state": "base"})
	originalForker := NewEnvironmentForker(resumeDriver, 1)
	resumedChild, resumedCleanup, _, err := originalForker.Fork(ctx, parentEnv, "resume")
	if err != nil {
		t.Fatal(err)
	}
	resumeDriver.setChild(resumedChild.Ref(), "state", "resumed")
	restartedForker := NewEnvironmentForker(resumeDriver, 1) // no process-local child bookkeeping
	sibling := tool.MustEnvironment(session.EnvironmentRef{Kind: Kind, ID: "sibling@9"}, memfs.NewWorkspace("/sibling"), memledger.New(), namespaceRunner{namespace: "sibling"})
	if err := restartedForker.Merge(ctx, resumedChild, sibling); !errors.Is(err, ErrInvalidFork) {
		t.Fatalf("cross-parent resumed Merge() error = %v, want ErrInvalidFork", err)
	}
	if got := resumeDriver.parentSnapshot()["state"]; got != "base" {
		t.Fatalf("cross-parent resume changed original parent to %q", got)
	}
	if err := restartedForker.Merge(ctx, resumedChild, parentEnv); err != nil {
		t.Fatalf("exact-parent resumed Merge(): %v", err)
	}
	if got := resumeDriver.parentSnapshot()["state"]; got != "resumed" {
		t.Fatalf("resumed merge state = %q", got)
	}
	if err := resumedCleanup(); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"cancellation", "timeout", "background-drain"} {
		t.Run(path, func(t *testing.T) {
			parentEnv := tool.MustEnvironment(session.EnvironmentRef{Kind: Kind, ID: "term-parent@1"}, memfs.NewWorkspace("/workspace"), memledger.New(), namespaceRunner{namespace: "parent"})
			driver := newMergeForkDriver(parentEnv.Ref(), map[string]string{"state": "parent"})
			forker := NewEnvironmentForker(driver, 1)
			cancelled, cancel := context.WithCancel(ctx)
			childEnv, cleanup, _, err := forker.Fork(cancelled, parentEnv, path)
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			if err := cleanup(); err != nil {
				t.Fatalf("cleanup after %s: %v", path, err)
			}
			if !driver.destroyed(childEnv.Ref()) || driver.destroyContextCancelled() {
				t.Fatalf("%s leaked child or forwarded cancelled cleanup context", path)
			}
		})
	}

	// A cleanup-pending child is reconciled generation-exactly; its ready parent
	// remains live and available for the existing resume path.
	child.State = EnvironmentCleanupPending
	child.Tombstone = true
	child.DeleteReason = DeleteRollback
	if err := restarted.Save(ctx, child); err != nil {
		t.Fatal(err)
	}
	runtimeState := &lifecycleRuntime{live: map[string]RuntimeStatus{
		parent.EnvironmentID: exactRuntimeStatus(parent),
		child.EnvironmentID:  exactRuntimeStatus(child),
	}}
	if err := NewReconciler(restarted, runtimeState, &lifecycleWorktrees{}).Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	gotChild, _ := restarted.Lookup(ctx, child.EnvironmentID)
	gotParent, _ := restarted.Lookup(ctx, parent.EnvironmentID)
	if gotChild.State != EnvironmentDestroyed || gotParent.State != EnvironmentReady || runtimeState.destroyCalls != 1 {
		t.Fatalf("reconciliation crossed parent/child lifecycle: child=%+v parent=%+v destroys=%d", gotChild, gotParent, runtimeState.destroyCalls)
	}
}
