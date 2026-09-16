package microvm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	gomicrovmnet "github.com/stacklok/go-microvm/net"

	"github.com/stacklok/mecatl/engine/tool"
)

func TestMicroVMMVP_Scenario2_DurableSingletonRegistryReattachesOrFailsLoudly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	stateRoot := filepath.Join(root, "state")
	verified := repositoryVerifiedArtifacts(t, root)
	runtime := newFakeRepositoryVMRuntime()

	const callers = 8
	results := make(chan RepositoryVMRecord, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			registry, err := OpenRepositoryVMRegistry(stateRoot, runtime)
			if err != nil {
				t.Errorf("open registry: %v", err)
				return
			}
			result, err := registry.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: testArtifactSnapshot(verified)})
			if err != nil {
				t.Errorf("concurrent ensure: %v", err)
				return
			}
			results <- result.Record
		}()
	}
	wg.Wait()
	close(results)

	var admitted RepositoryVMRecord
	for record := range results {
		if admitted.Generation == 0 {
			admitted = record
		}
		if record != admitted {
			t.Fatalf("concurrent first use diverged: admitted=%+v got=%+v", admitted, record)
		}
	}
	if runtime.startCount() != 1 {
		t.Fatalf("repository runtime starts = %d, want 1", runtime.startCount())
	}

	restarted, err := OpenRepositoryVMRegistry(stateRoot, runtime)
	if err != nil {
		t.Fatal(err)
	}
	reattached, err := restarted.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository})
	if err != nil {
		t.Fatalf("reattach exact healthy generation: %v", err)
	}
	if !reattached.Reattached || reattached.Record != admitted || runtime.startCount() != 1 {
		t.Fatalf("restart minted a replacement: result=%+v starts=%d", reattached, runtime.startCount())
	}

	runtime.mu.Lock()
	delete(runtime.statuses, admitted.VMID)
	runtime.mu.Unlock()
	if _, err := restarted.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: testArtifactSnapshot(verified)}); err == nil {
		t.Fatal("missing runtime state minted a replacement")
	}
	if runtime.startCount() != 1 {
		t.Fatalf("failed reattach started replacement generation: starts=%d", runtime.startCount())
	}

	production := newRepositoryAttachmentFixture(t)
	attached := production.attach(t)
	ref, worktree := attached.Environment.Ref(), attached.Logical.WorktreePath
	rootfs := attached.Logical.Repository.RootFSPath
	if err := production.composition.Attachments.Detach(ref); err != nil {
		t.Fatalf("detach before daemon restart: %v", err)
	}
	restartedComposition, err := NewRepositoryComposition(production.stateRoot, production.runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restartedComposition.Attachments.Reattach(t.Context(), LogicalEnvironmentRequest{Owner: "operator", Checkout: worktree}, ref); !errors.Is(err, ErrRepositoryVMInconsistent) {
		t.Fatalf("daemon restart without live network backend = %v, want inconsistent generation", err)
	}
	if production.backend.starts != 1 {
		t.Fatalf("failed restart minted a replacement generation: starts=%d", production.backend.starts)
	}
	for _, retained := range []string{worktree, rootfs} {
		if _, err := os.Stat(retained); err != nil {
			t.Fatalf("failed restart did not preserve %s: %v", retained, err)
		}
	}
}

func TestMicroVMMVP_Scenario5_SessionsAndChildrenReuseRepositoryVM(t *testing.T) {
	t.Parallel()
	fixture := newRepositoryAttachmentFixture(t)
	first := fixture.attach(t)
	second := fixture.attach(t)
	defer first.Close()
	defer second.Close()

	if first.Logical.Repository.VMID != second.Logical.Repository.VMID || first.Environment.Ref() == second.Environment.Ref() || first.Logical.WorktreePath == second.Logical.WorktreePath {
		t.Fatalf("sessions did not share only the repository VM: first=%+v second=%+v", first.Logical, second.Logical)
	}

	children := make([]tool.Environment, 0, 3)
	cleanups := make([]func() error, 0, 3)
	for _, label := range []string{"subagent-read-only", "parallel-branch", "team-member"} {
		child, cleanup, _, err := fixture.composition.Attachments.Fork(t.Context(), first.Environment, label)
		if err != nil {
			t.Fatalf("fork %s: %v", label, err)
		}
		children, cleanups = append(children, child), append(cleanups, cleanup)
	}
	defer func() {
		for _, cleanup := range cleanups {
			_ = cleanup()
		}
	}()
	seen := map[string]bool{first.Environment.Ref().ID: true, second.Environment.Ref().ID: true}
	for _, child := range children {
		attachment := fixture.composition.Attachments.lookup(child.Ref())
		if attachment == nil || attachment.Logical.Repository.VMID != first.Logical.Repository.VMID || attachment.Logical.WorktreePath == first.Logical.WorktreePath || seen[child.Ref().ID] {
			t.Fatalf("isolated child did not receive a distinct logical attachment in the repository VM: ref=%+v attachment=%+v", child.Ref(), attachment)
		}
		seen[child.Ref().ID] = true
	}
}

func TestRepositoryForkCapturesDirtyParentSnapshot(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	parent := fixture.attach(t)
	defer parent.Close()
	parentRoot := parent.Logical.WorktreePath
	if err := os.WriteFile(filepath.Join(parentRoot, "tracked.txt"), []byte("unstaged parent bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentRoot, "staged.txt"), []byte("staged parent bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexecForLogicalTest(t.Context(), parentRoot, "add", "staged.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentRoot, "untracked.txt"), []byte("untracked parent bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	child, cleanup, _, err := fixture.composition.Attachments.Fork(t.Context(), parent.Environment, "dirty-parent")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	childRoot := fixture.composition.Attachments.lookup(child.Ref()).Logical.WorktreePath
	for name, want := range map[string]string{
		"tracked.txt": "unstaged parent bytes\n", "staged.txt": "staged parent bytes\n", "untracked.txt": "untracked parent bytes\n",
	} {
		got, readErr := os.ReadFile(filepath.Join(childRoot, name))
		if readErr != nil || string(got) != want {
			t.Fatalf("forked %s = %q, %v; want dirty snapshot bytes %q", name, got, readErr, want)
		}
	}
}

func TestMicroVMMVP_Scenario5_CloseDetachesWithoutDestroyingRepositoryVM(t *testing.T) {
	t.Parallel()
	fixture := newRepositoryAttachmentFixture(t)
	first := fixture.attach(t)
	second := fixture.attach(t)
	firstWorktree := first.Logical.WorktreePath
	rootfs := first.Logical.Repository.RootFSPath
	vmID := first.Logical.Repository.VMID

	if err := first.Close(); err != nil {
		t.Fatalf("close first session: %v", err)
	}
	if _, err := os.Stat(firstWorktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed logical worktree remains: %v", err)
	}
	if _, err := os.Stat(rootfs); err != nil {
		t.Fatalf("closing attachment destroyed shared rootfs: %v", err)
	}
	if second.Logical.Repository.VMID != vmID {
		t.Fatalf("sibling moved repository VM: got %q want %q", second.Logical.Repository.VMID, vmID)
	}
	if got, err := second.Environment.Workspace().Read(t.Context(), "tracked.txt"); err != nil || string(got) != "source\n" {
		t.Fatalf("sibling attachment stopped after close: %q, %v", got, err)
	}
	if fixture.backend.starts != 1 {
		t.Fatalf("close restarted or destroyed repository VM: starts=%d", fixture.backend.starts)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rootfs); err != nil {
		t.Fatalf("last logical close destroyed repository rootfs: %v", err)
	}
}

func TestMicroVMMVP_Scenario5_BasicExistingMergeBehavior(t *testing.T) {
	t.Parallel()
	fixture := newRepositoryAttachmentFixture(t)
	parent := fixture.attach(t)
	defer parent.Close()

	child, cleanup, _, err := fixture.composition.Attachments.Fork(t.Context(), parent.Environment, "merge-success")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.composition.Attachments.lookup(child.Ref()).Logical.WorktreePath, "child.txt"), []byte("applied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	childRoot := fixture.composition.Attachments.lookup(child.Ref()).Logical.WorktreePath
	if err := os.WriteFile(filepath.Join(childRoot, "tracked.txt"), []byte("replaced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(childRoot, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.composition.Attachments.Merge(t.Context(), child, parent.Environment); err != nil {
		t.Fatalf("merge non-conflicting child: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(parent.Logical.WorktreePath, "child.txt")); err != nil || string(got) != "applied\n" {
		t.Fatalf("merged parent bytes = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(parent.Logical.WorktreePath, "tracked.txt")); err != nil || string(got) != "replaced\n" {
		t.Fatalf("merged replacement = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(parent.Logical.WorktreePath, "README.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("merged deletion remains: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}

	conflict, conflictCleanup, _, err := fixture.composition.Attachments.Fork(t.Context(), parent.Environment, "merge-conflict")
	if err != nil {
		t.Fatal(err)
	}
	defer conflictCleanup()
	conflictAttachment := fixture.composition.Attachments.lookup(conflict.Ref())
	if err := os.WriteFile(filepath.Join(conflictAttachment.Logical.WorktreePath, "tracked.txt"), []byte("child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parentConflictPath := filepath.Join(parent.Logical.WorktreePath, "tracked.txt")
	if err := os.WriteFile(parentConflictPath, []byte("parent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parentBefore, err := os.ReadFile(parentConflictPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.composition.Attachments.Merge(t.Context(), conflict, parent.Environment); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("conflicting merge = %v, want ErrMergeConflict", err)
	}
	parentAfter, err := os.ReadFile(parentConflictPath)
	if err != nil || !bytes.Equal(parentAfter, parentBefore) {
		t.Fatalf("conflicting merge changed parent bytes: before=%q after=%q err=%v", parentBefore, parentAfter, err)
	}
	if _, err := os.Stat(conflictAttachment.Logical.WorktreePath); err != nil {
		t.Fatalf("conflict did not preserve child: %v", err)
	}
}

func TestRepositoryMergeSerializesConcurrentCalls(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	parent := fixture.attach(t)
	defer parent.Close()
	first, cleanupFirst, _, err := fixture.composition.Attachments.Fork(t.Context(), parent.Environment, "serial-first")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupFirst()
	second, cleanupSecond, _, err := fixture.composition.Attachments.Fork(t.Context(), parent.Environment, "serial-second")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSecond()

	manager := fixture.composition.Attachments
	attempted := make(chan struct{}, 2)
	entered := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	manager.beforeMergeLock = func() { attempted <- struct{}{} }
	manager.mergeWorktrees = func(context.Context, string, string, string) error {
		entered <- struct{}{}
		<-release
		return nil
	}
	errs := make(chan error, 2)
	go func() { errs <- manager.Merge(t.Context(), first, parent.Environment) }()
	<-attempted
	<-entered
	go func() { errs <- manager.Merge(t.Context(), second, parent.Environment) }()
	<-attempted
	select {
	case <-entered:
		t.Fatal("second same-process merge entered while first merge was active")
	default:
	}
	release <- struct{}{}
	if err := <-errs; err != nil {
		t.Fatalf("first merge: %v", err)
	}
	<-entered
	release <- struct{}{}
	if err := <-errs; err != nil {
		t.Fatalf("second merge: %v", err)
	}
}

type repositoryAttachmentFixture struct {
	composition   *RepositoryComposition
	backend       *repositoryCompositionBackend
	repository    string
	verified      VerifiedArtifacts
	stateRoot     string
	runtimeConfig RepositoryRuntimeConfig
}

func newRepositoryAttachmentFixture(t *testing.T) *repositoryAttachmentFixture {
	t.Helper()
	root := t.TempDir()
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	if err := os.Chmod(filepath.Join(repository, "README.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexecForLogicalTest(t.Context(), repository, "add", "tracked.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexecForLogicalTest(t.Context(), repository, "commit", "-m", "tracked"); err != nil {
		t.Fatal(err)
	}
	backend := &repositoryCompositionBackend{}
	network := NewNetworkController(func() gomicrovmnet.Provider { return &fakeNetworkProvider{socket: filepath.Join(root, "network.sock")} }, &fakeGuestNetwork{})
	stateRoot := filepath.Join(root, "state")
	runtimeConfig := RepositoryRuntimeConfig{Backend: backend, Network: network, GuestEgress: GuestEgressPolicy{Mode: EgressDenyAll}, DialGuest: backend.dialData, DialControl: backend.dialControl}
	composition, err := NewRepositoryComposition(stateRoot, runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	return &repositoryAttachmentFixture{
		composition: composition, backend: backend, repository: repository,
		verified: repositoryVerifiedArtifacts(t, root), stateRoot: stateRoot, runtimeConfig: runtimeConfig,
	}
}

func (f *repositoryAttachmentFixture) attach(t *testing.T) *RepositoryAttachment {
	t.Helper()
	attachment, err := f.composition.Attachments.Attach(t.Context(), LogicalEnvironmentRequest{Owner: "operator", Checkout: f.repository, Artifacts: testArtifactSnapshot(f.verified)})
	if err != nil {
		t.Fatalf("attach repository session: %v", err)
	}
	return attachment
}
