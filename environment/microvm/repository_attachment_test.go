package microvm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	gomicrovmnet "github.com/stacklok/go-microvm/net"
	gomicrovmvirtiofs "github.com/stacklok/go-microvm/virtiofs"
	"golang.org/x/sys/unix"

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
	attached := production.attachPersisted(t)
	ref, worktree := attached.Environment.Ref(), attached.Logical.WorktreePath
	rootfs := attached.Logical.Repository.RootFSPath
	if err := production.composition.Attachments.Detach(ref); err != nil {
		t.Fatalf("detach before daemon restart: %v", err)
	}
	restartedComposition, err := NewRepositoryComposition(production.stateRoot, production.runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restartedComposition.Attachments.Reattach(t.Context(), LogicalEnvironmentRequest{Owner: "operator", Checkout: worktree}, ref)
	if err != nil {
		t.Fatalf("daemon restart did not recover retained repository: %v", err)
	}
	defer recovered.Logical.Detach()
	if recovered.Environment.Ref() != ref || recovered.Logical.Repository.Generation != attached.Logical.Repository.Generation || recovered.Logical.Repository.Boot.Generation == attached.Logical.Repository.Boot.Generation {
		t.Fatalf("recovery changed logical placement or reused boot authority: old=%+v new=%+v", attached.Logical.Repository, recovered.Logical.Repository)
	}
	if production.backend.starts != 2 {
		t.Fatalf("daemon restart boots = %d, want one replacement", production.backend.starts)
	}
	for _, retained := range []string{worktree, rootfs} {
		if _, err := os.Stat(retained); err != nil {
			t.Fatalf("failed restart did not preserve %s: %v", retained, err)
		}
	}
}

func TestRepositoryRecoveryPreservesExactPlacementAndMutableState(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attachPersisted(t)
	ref := attachment.Environment.Ref()
	oldRecord := attachment.Logical.Repository
	tracked := filepath.Join(attachment.Logical.WorktreePath, "tracked.txt")
	untracked := filepath.Join(attachment.Logical.WorktreePath, "untracked.txt")
	if err := os.WriteFile(tracked, []byte("staged after boot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexecForLogicalTest(t.Context(), attachment.Logical.WorktreePath, "add", "tracked.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untracked, []byte("untracked after boot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootMarkers := map[string]string{
		filepath.Join(oldRecord.RootFSPath, "home", "guest", "recovery-home"):            "home-state",
		filepath.Join(oldRecord.RootFSPath, "home", "guest", ".cache", "recovery-cache"): "cache-state",
	}
	for path, value := range rootMarkers {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.composition.Attachments.Detach(ref); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewRepositoryComposition(fixture.stateRoot, fixture.runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.Attachments.Reattach(t.Context(), LogicalEnvironmentRequest{Owner: "operator", Checkout: fixture.repository}, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Logical.Detach()
	newRecord := recovered.Logical.Repository
	if recovered.Environment.Ref() != ref || newRecord.Generation != oldRecord.Generation || newRecord.RootFSPath != oldRecord.RootFSPath {
		t.Fatalf("recovery changed durable placement: ref=%+v record=%+v", recovered.Environment.Ref(), newRecord)
	}
	if newRecord.Boot.Generation == oldRecord.Boot.Generation || newRecord.VMID == oldRecord.VMID || newRecord.AuthorityDigest == oldRecord.AuthorityDigest {
		t.Fatalf("recovery reused replaceable boot identity: old=%+v new=%+v", oldRecord.Boot, newRecord.Boot)
	}
	status, err := gitexecForLogicalTest(t.Context(), recovered.Logical.WorktreePath, "status", "--porcelain", "--untracked-files=all")
	if err != nil || !strings.Contains(string(status), "M  tracked.txt") || !strings.Contains(string(status), "?? untracked.txt") {
		t.Fatalf("recovered Git index/worktree state = %q, %v", status, err)
	}
	for path, value := range rootMarkers {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != value {
			t.Fatalf("recovered rootfs marker %q = %q, %v", path, got, err)
		}
	}
	if fixture.backend.starts != 2 {
		t.Fatalf("recovery starts = %d, want exactly one replacement", fixture.backend.starts)
	}
}

func TestRepositoryReattachRejectsCallerSelectedDifferentRepository(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attachPersisted(t)
	ref := attachment.Environment.Ref()
	marker := filepath.Join(attachment.Logical.WorktreePath, "reattach-marker")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.composition.Attachments.Detach(ref); err != nil {
		t.Fatal(err)
	}
	other, _, _ := repositoryIdentityFixture(t, t.TempDir(), "other-repository")
	starts := fixture.backend.starts
	if _, err := fixture.composition.Attachments.Reattach(t.Context(), LogicalEnvironmentRequest{Owner: "operator", Checkout: other}, ref); err == nil {
		t.Fatal("reattach accepted caller-selected different repository")
	}
	if fixture.backend.starts != starts {
		t.Fatalf("forged reattach started a different repository: starts=%d want=%d", fixture.backend.starts, starts)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "preserve" {
		t.Fatalf("forged reattach changed retained state: %q, %v", got, err)
	}
}

func TestRepositoryRecoveryRejectsPolicyDriftWithoutMutatingState(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attachPersisted(t)
	ref := attachment.Environment.Ref()
	marker := filepath.Join(attachment.Logical.WorktreePath, "policy-drift-marker")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.composition.Attachments.Detach(ref); err != nil {
		t.Fatal(err)
	}
	changed := fixture.runtimeConfig
	changed.GuestEgress = GuestEgressPolicy{Mode: EgressPermissive}
	restarted, err := NewRepositoryComposition(fixture.stateRoot, changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Attachments.Reattach(t.Context(), LogicalEnvironmentRequest{Owner: "operator", Checkout: fixture.repository}, ref); !errors.Is(err, ErrRepositoryVMInconsistent) {
		t.Fatalf("policy drift recovery error = %v", err)
	}
	if fixture.backend.starts != 1 {
		t.Fatalf("policy drift started replacement runtime: starts=%d", fixture.backend.starts)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "preserve" {
		t.Fatalf("policy drift changed retained state: %q, %v", got, err)
	}
}

func TestRepositoryRecoveryConcurrentEnsureStartsOneBoot(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attachPersisted(t)
	stableGeneration := attachment.Logical.Repository.Generation
	if err := fixture.composition.Attachments.Detach(attachment.Environment.Ref()); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewRepositoryComposition(fixture.stateRoot, fixture.runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	const callers = 8
	records := make(chan RepositoryVMRecord, callers)
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := restarted.Registry.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: fixture.repository})
			if err != nil {
				errs <- err
				return
			}
			records <- result.Record
		}()
	}
	wait.Wait()
	close(records)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first RepositoryVMRecord
	for record := range records {
		if first.Generation == 0 {
			first = record
		}
		if record != first || record.Generation != stableGeneration {
			t.Fatalf("concurrent recovery diverged: first=%+v record=%+v", first, record)
		}
	}
	if fixture.backend.starts != 2 {
		t.Fatalf("concurrent recovery starts = %d, want one replacement", fixture.backend.starts)
	}
}

func TestRepositoryRecoveryRejectsCorruptRetainedArtifact(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attachPersisted(t)
	ref := attachment.Environment.Ref()
	artifactFile := filepath.Join(attachment.Logical.Repository.Artifacts.Runtime.Path, "artifact")
	if err := os.WriteFile(artifactFile, []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fixture.composition.Attachments.Detach(ref); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewRepositoryComposition(fixture.stateRoot, fixture.runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Attachments.Reattach(t.Context(), LogicalEnvironmentRequest{Owner: "operator", Checkout: fixture.repository}, ref); !errors.Is(err, ErrRepositoryVMInconsistent) {
		t.Fatalf("corrupt artifact recovery error = %v", err)
	}
	if fixture.backend.starts != 1 {
		t.Fatalf("corrupt artifact started replacement runtime: starts=%d", fixture.backend.starts)
	}
	if got, err := os.ReadFile(artifactFile); err != nil || string(got) != "corrupt" {
		t.Fatalf("corrupt artifact was replaced: %q, %v", got, err)
	}
}

func TestRepositoryRecoveryRetriesFailedReplacementBoot(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attachPersisted(t)
	ref := attachment.Environment.Ref()
	stableGeneration := attachment.Logical.Repository.Generation
	if err := fixture.composition.Attachments.Detach(ref); err != nil {
		t.Fatal(err)
	}
	fixture.backend.failStart = 1
	restarted, err := NewRepositoryComposition(fixture.stateRoot, fixture.runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Attachments.Reattach(t.Context(), LogicalEnvironmentRequest{Owner: "operator", Checkout: fixture.repository}, ref); err == nil {
		t.Fatal("injected replacement boot failure was accepted")
	}
	recovered, err := restarted.Attachments.Reattach(t.Context(), LogicalEnvironmentRequest{Owner: "operator", Checkout: fixture.repository}, ref)
	if err != nil {
		t.Fatalf("retry replacement boot: %v", err)
	}
	defer recovered.Logical.Detach()
	if recovered.Environment.Ref() != ref || recovered.Logical.Repository.Generation != stableGeneration || fixture.backend.starts != 3 {
		t.Fatalf("replacement retry changed placement or start count: ref=%+v record=%+v starts=%d", recovered.Environment.Ref(), recovered.Logical.Repository, fixture.backend.starts)
	}
}

func TestMicroVMMVP_Scenario5_SessionsAndChildrenReuseRepositoryVM(t *testing.T) {
	t.Parallel()
	fixture := newRepositoryAttachmentFixture(t)
	first := fixture.attachPersisted(t)
	second := fixture.attachPersisted(t)
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
	parent := fixture.attachPersisted(t)
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
	first := fixture.attachPersisted(t)
	second := fixture.attachPersisted(t)
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
	parent := fixture.attachPersisted(t)
	defer parent.Close()
	manager := fixture.composition.Attachments
	var refreshedChildHostMode os.FileMode
	var prepareCalls int
	prepare := func(ctx context.Context, root, relative string) error {
		prepareCalls++
		if root != parent.Logical.WorktreePath || relative != "." {
			t.Fatalf("merge ownership preparation = (%q, %q), want parent worktree and dot", root, relative)
		}
		if info, err := os.Stat(filepath.Join(root, "child.txt")); err == nil {
			refreshedChildHostMode = info.Mode().Perm()
		}
		return gomicrovmvirtiofs.PrepareOwnership(ctx, root, relative, repositoryGuestOwnershipID, repositoryGuestOwnershipID)
	}
	manager.mergeWorktrees = func(ctx context.Context, parentPath, childPath, base string) error {
		return mergeRepositoryWorktreesWithOwnership(ctx, parentPath, childPath, base, prepare)
	}
	if err := gomicrovmvirtiofs.PrepareOwnership(t.Context(), parent.Logical.WorktreePath, ".", repositoryGuestOwnershipID, repositoryGuestOwnershipID); err != nil {
		t.Fatalf("prepare parent ownership fixture: %v", err)
	}
	trackedPath := filepath.Join(parent.Logical.WorktreePath, "tracked.txt")
	guestMode := overrideStatForTest(t, trackedPath)
	if len(guestMode) < 3 {
		t.Fatalf("unexpected override_stat value %q", guestMode)
	}
	guestMode = guestMode[:len(guestMode)-3] + "640"
	if err := unix.Setxattr(trackedPath, "user.containers.override_stat", []byte(guestMode), 0); err != nil {
		t.Fatal(err)
	}
	trackedBefore, err := os.Stat(trackedPath)
	if err != nil {
		t.Fatal(err)
	}
	stablePath := filepath.Join(parent.Logical.WorktreePath, "stable.txt")
	stableGuestMode := overrideStatForTest(t, stablePath)
	if len(stableGuestMode) < 3 {
		t.Fatalf("unexpected stable override_stat value %q", stableGuestMode)
	}
	stableGuestMode = stableGuestMode[:len(stableGuestMode)-3] + "640"
	if err := unix.Setxattr(stablePath, "user.containers.override_stat", []byte(stableGuestMode), 0); err != nil {
		t.Fatal(err)
	}
	stableBefore, err := os.Stat(stablePath)
	if err != nil {
		t.Fatal(err)
	}

	child, cleanup, _, err := fixture.composition.Attachments.Fork(t.Context(), parent.Environment, "merge-success")
	if err != nil {
		t.Fatal(err)
	}
	childRoot := fixture.composition.Attachments.lookup(child.Ref()).Logical.WorktreePath
	if err := os.WriteFile(filepath.Join(childRoot, "child.txt"), []byte("applied\n"), 0o700); err != nil {
		t.Fatal(err)
	}
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
	if prepareCalls != 1 {
		t.Fatalf("merge ownership preparation calls = %d, want 1", prepareCalls)
	}
	trackedAfter, err := os.Stat(trackedPath)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(trackedBefore, trackedAfter) {
		t.Fatal("content-changing patch unexpectedly retained tracked file inode")
	}
	if got := overrideStatForTest(t, trackedPath); got == guestMode || !strings.HasSuffix(got, "644") {
		t.Fatalf("replacement file ownership = %q, want host-derived mode without stale %q", got, guestMode)
	}
	if got := overrideStatForTest(t, filepath.Join(parent.Logical.WorktreePath, "child.txt")); !strings.HasPrefix(got, "65532:65532:") || !strings.HasSuffix(got, "755") {
		t.Fatalf("new executable merged file ownership = %q, want host-derived executable mode", got)
	}
	if info, err := os.Stat(filepath.Join(parent.Logical.WorktreePath, "child.txt")); err != nil || info.Mode().Perm() != refreshedChildHostMode || info.Mode().Perm() != 0o755 {
		t.Fatalf("ownership refresh changed new file host mode: before=%o after=%v err=%v", refreshedChildHostMode, infoMode(info), err)
	}
	stableAfter, err := os.Stat(stablePath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(stableBefore, stableAfter) {
		t.Fatal("unmodified stable file inode changed during merge")
	}
	if got := overrideStatForTest(t, stablePath); got != stableGuestMode {
		t.Fatalf("merge refresh changed surviving inode guest mode: got %q want %q", got, stableGuestMode)
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

func TestRepositoryMergeReportsOwnershipRefreshFailureAfterPatchApplied(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	parent := fixture.attachPersisted(t)
	defer parent.Close()
	child, cleanup, _, err := fixture.composition.Attachments.Fork(t.Context(), parent.Environment, "refresh-failure")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	childPath := fixture.composition.Attachments.lookup(child.Ref()).Logical.WorktreePath
	if err := os.WriteFile(filepath.Join(childPath, "applied-before-refresh-error.txt"), []byte("applied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refreshErr := &os.PathError{Op: "setxattr", Path: filepath.Join(parent.Logical.WorktreePath, "applied-before-refresh-error.txt"), Err: errors.New("injected ownership refresh failure")}
	fixture.composition.Attachments.mergeWorktrees = func(ctx context.Context, parentPath, childPath, base string) error {
		return mergeRepositoryWorktreesWithOwnership(ctx, parentPath, childPath, base,
			func(context.Context, string, string) error { return refreshErr },
		)
	}
	if err := fixture.composition.Attachments.Merge(t.Context(), child, parent.Environment); !errors.Is(err, refreshErr) || !strings.Contains(err.Error(), "patch already applied") {
		t.Fatalf("Merge error = %v, want honest already-applied patch path error", err)
	}
	if got, err := os.ReadFile(filepath.Join(parent.Logical.WorktreePath, "applied-before-refresh-error.txt")); err != nil || string(got) != "applied\n" {
		t.Fatalf("parent does not expose already-applied patch: %q, %v", got, err)
	}
	if _, err := os.Stat(childPath); err != nil {
		t.Fatalf("refresh failure removed recoverable child: %v", err)
	}
}

func TestRepositoryMergeSerializesConcurrentCalls(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	parent := fixture.attachPersisted(t)
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

func overrideStatForTest(t *testing.T, path string) string {
	t.Helper()
	size, err := unix.Getxattr(path, "user.containers.override_stat", nil)
	if err != nil {
		t.Fatalf("read override_stat size for %s: %v", path, err)
	}
	value := make([]byte, size)
	if _, err := unix.Getxattr(path, "user.containers.override_stat", value); err != nil {
		t.Fatalf("read override_stat for %s: %v", path, err)
	}
	return string(value)
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
	if err := os.WriteFile(filepath.Join(repository, "stable.txt"), []byte("stable fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexecForLogicalTest(t.Context(), repository, "add", "tracked.txt", "stable.txt"); err != nil {
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

func (f *repositoryAttachmentFixture) attachPersisted(t *testing.T) *RepositoryAttachment {
	t.Helper()
	attachment := f.attach(t)
	binding := attachment.Logical.Binding
	environmentID, generation, err := parseEnvironmentRef(attachment.Logical.Ref)
	if err != nil {
		t.Fatal(err)
	}
	binding.EnvironmentID = environmentID
	binding.Generation = generation
	binding.AssignedRoot = ""
	if err := f.composition.Attachments.register(binding, attachment); err != nil {
		_ = attachment.Close()
		t.Fatalf("persist repository session attachment: %v", err)
	}
	return attachment
}
