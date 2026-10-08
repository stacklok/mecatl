package microvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/gitexec"
)

func TestRepositoryFirstLaunchArtifactSnapshotIsSingleUse(t *testing.T) {
	root := t.TempDir()
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	verified := repositoryVerifiedArtifacts(t, root)
	runtime := newFakeRepositoryVMRuntime()
	registry, err := OpenRepositoryVMRegistry(filepath.Join(root, "state"), runtime)
	if err != nil {
		t.Fatal(err)
	}
	calls, releases := 0, 0
	snapshot := func(context.Context) (VerifiedArtifacts, func(), error) {
		calls++
		return verified, func() { releases++ }, nil
	}
	if _, err := registry.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: snapshot}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || releases != 1 {
		t.Fatalf("first launch snapshot calls/releases = %d/%d, want 1/1", calls, releases)
	}
	if _, err := registry.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: func(context.Context) (VerifiedArtifacts, func(), error) {
		t.Fatal("healthy repository reuse requested another artifact snapshot")
		return VerifiedArtifacts{}, nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || runtime.startCount() != 1 {
		t.Fatalf("reuse snapshot calls/starts = %d/%d, want 1/1", calls, runtime.startCount())
	}
}

func TestRepositoryArtifactSnapshotErrorRetainsCleanupOwnership(t *testing.T) {
	root := t.TempDir()
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	registry, err := OpenRepositoryVMRegistry(filepath.Join(root, "state"), newFakeRepositoryVMRuntime())
	if err != nil {
		t.Fatal(err)
	}
	acquired, cleaned := false, false
	snapshotErr := errors.New("snapshot acquisition failed")
	_, err = registry.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: func(context.Context) (VerifiedArtifacts, func(), error) {
		acquired = true
		cleaned = true
		return VerifiedArtifacts{}, func() { t.Fatal("Ensure invoked release returned with an acquisition error") }, snapshotErr
	}})
	if !errors.Is(err, snapshotErr) || !acquired || !cleaned {
		t.Fatalf("snapshot error ownership = acquired:%v cleaned:%v err:%v", acquired, cleaned, err)
	}
}

func TestRepositoryArtifactSnapshotReleaseFollowsBackendConsumption(t *testing.T) {
	root := t.TempDir()
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	verified := repositoryVerifiedArtifacts(t, root)
	releases := 0
	runtime := &artifactConsumingRuntime{fakeRepositoryVMRuntime: newFakeRepositoryVMRuntime(), consume: func(got VerifiedArtifacts) {
		if releases != 0 {
			t.Fatal("artifact snapshot was released before backend consumption")
		}
		for _, path := range []string{got.Runtime.Path, got.Firmware.Path, got.ExecutionImage.Path, got.GuestAgent.Path} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("backend consumed unavailable snapshot path %q: %v", path, err)
			}
		}
	}}
	registry, err := OpenRepositoryVMRegistry(filepath.Join(root, "state"), runtime)
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: func(context.Context) (VerifiedArtifacts, func(), error) {
		return verified, func() { releases++ }, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if releases != 1 {
		t.Fatalf("artifact snapshot releases = %d, want exactly one after backend consumption", releases)
	}
}

func TestRepositoryFirstLaunchRejectsMissingArtifactSnapshot(t *testing.T) {
	root := t.TempDir()
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	registry, err := OpenRepositoryVMRegistry(filepath.Join(root, "state"), newFakeRepositoryVMRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository}); err == nil {
		t.Fatal("first repository launch accepted no artifact validator")
	}
}

func TestMicroVMMVP_Scenario2_CanonicalRepositoryIdentitySelectsSingletonVM(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	repository, linked, symlink := repositoryIdentityFixtureWithLinkedWorktree(t, root, "repository")
	other, _, _ := repositoryIdentityFixture(t, root, "other", "repository")

	canonical, err := ResolveRepositoryIdentity(ctx, "operator-a", repository, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, checkout := range []string{linked, symlink} {
		got, err := ResolveRepositoryIdentity(ctx, "operator-a", checkout, stateRoot)
		if err != nil {
			t.Fatalf("resolve equivalent checkout %q: %v", checkout, err)
		}
		if got != canonical {
			t.Fatalf("equivalent checkout identity = %+v, want %+v", got, canonical)
		}
	}
	otherRepository, err := ResolveRepositoryIdentity(ctx, "operator-a", other, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	otherOperator, err := ResolveRepositoryIdentity(ctx, "operator-b", repository, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if otherRepository.Key == canonical.Key || otherOperator.Key == canonical.Key {
		t.Fatalf("different repository/operator collided: canonical=%+v other-repository=%+v other-operator=%+v", canonical, otherRepository, otherOperator)
	}

	verified := repositoryVerifiedArtifacts(t, root)
	runtime := newFakeRepositoryVMRuntime()
	registry, err := OpenRepositoryVMRegistry(stateRoot, runtime)
	if err != nil {
		t.Fatal(err)
	}
	first, err := registry.Ensure(ctx, RepositoryVMRequest{Owner: "operator-a", Checkout: repository, Artifacts: testArtifactSnapshot(verified)})
	if err != nil {
		t.Fatalf("ensure repository VM: %v", err)
	}
	second, err := registry.Ensure(ctx, RepositoryVMRequest{Owner: "operator-a", Checkout: linked})
	if err != nil {
		t.Fatalf("ensure from linked worktree: %v", err)
	}
	if got := runtime.startCount(); first.Record.Generation != second.Record.Generation || first.Record.VMID != second.Record.VMID || got != 1 {
		t.Fatalf("equivalent checkouts did not select singleton VM: first=%+v second=%+v starts=%d", first.Record, second.Record, got)
	}
}

func TestRepositoryHealthRejectsForeignExactStatusWithoutBootAuthority(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	runtime := newFakeRepositoryVMRuntime()
	registry, err := OpenRepositoryVMRegistry(filepath.Join(root, "state"), runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: testArtifactSnapshot(repositoryVerifiedArtifacts(t, root))}); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.forgeHealth = true
	runtime.mu.Unlock()
	if _, err := registry.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository}); !errors.Is(err, ErrRepositoryVMInconsistent) {
		t.Fatalf("foreign exact-status endpoint accepted: %v", err)
	}
}

func TestRepositoryVMRegistryReadyCommitFailureAbortsUnpublishedRuntime(t *testing.T) {
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	runtime := newFakeRepositoryVMRuntime()
	registry, err := OpenRepositoryVMRegistry(stateRoot, runtime)
	if err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("injected ready-record persistence failure")
	writeRecord := registry.writeRecord
	registry.writeRecord = func(directory *repositoryDirectory, record RepositoryVMRecord) error {
		if record.State == EnvironmentReady {
			return persistErr
		}
		return writeRecord(directory, record)
	}
	_, err = registry.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: testArtifactSnapshot(repositoryVerifiedArtifacts(t, root))})
	if !errors.Is(err, persistErr) {
		t.Fatalf("ready commit failure = %v, want injected persistence error", err)
	}
	if runtime.aborts != 1 || len(runtime.statuses) != 0 {
		t.Fatalf("unpublished runtime remained live: aborts=%d statuses=%v", runtime.aborts, runtime.statuses)
	}
	identity, err := ResolveRepositoryIdentity(t.Context(), "operator", repository, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := registry.validateIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := registry.openIdentityDirectory(validated, false)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	persisted, err := readRepositoryRecord(directory)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != EnvironmentProvisioning {
		t.Fatalf("failed ready commit replaced provisioning record: %+v", persisted)
	}
	if _, err := registry.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository}); !errors.Is(err, ErrRepositoryVMInconsistent) {
		t.Fatalf("provisioning generation was silently replaced: %v", err)
	}
	if runtime.startCount() != 1 {
		t.Fatalf("provisioning retry started a replacement: starts=%d", runtime.startCount())
	}
}

func TestRepositoryVMRegistryConcurrentEnsureConvergesAndPersistsRecord(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	verified := repositoryVerifiedArtifacts(t, root)
	runtime := newFakeRepositoryVMRuntime()

	const callers = 12
	results := make(chan RepositoryVMRecord, callers)
	errorsCh := make(chan error, callers)
	var wait sync.WaitGroup
	for i := 0; i < callers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			registry, err := OpenRepositoryVMRegistry(stateRoot, runtime)
			if err != nil {
				errorsCh <- err
				return
			}
			result, err := registry.Ensure(ctx, RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: testArtifactSnapshot(verified)})
			if err != nil {
				errorsCh <- err
				return
			}
			results <- result.Record
		}()
	}
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatalf("concurrent ensure: %v", err)
	}
	var admitted RepositoryVMRecord
	for record := range results {
		if admitted.Generation == 0 {
			admitted = record
		}
		if record != admitted {
			t.Fatalf("concurrent first use diverged: first=%+v got=%+v", admitted, record)
		}
	}
	if got := runtime.startCount(); got != 1 {
		t.Fatalf("runtime starts = %d, want 1", got)
	}

	identity, err := ResolveRepositoryIdentity(ctx, "operator", repository, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRepositoryVMRegistry(stateRoot, runtime)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Lookup(ctx, identity)
	if err != nil {
		t.Fatalf("lookup persisted record: %v", err)
	}
	if persisted != admitted {
		t.Fatalf("persisted record = %+v, want %+v", persisted, admitted)
	}
}

func TestRepositoryVMRegistryLookupRejectsForgedKeyTraversalBeforeLockCreation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	identity, err := ResolveRepositoryIdentity(t.Context(), "operator", repository, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	identity.Key = filepath.Join("..", "..", "..", "..", "outside")

	registry, err := OpenRepositoryVMRegistry(stateRoot, newFakeRepositoryVMRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Lookup(t.Context(), identity); err == nil {
		t.Fatal("Lookup accepted a forged repository key")
	}
	if _, err := os.Stat(filepath.Join(outside, "registry.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forged key created an outside lock: %v", err)
	}
}

func TestRepositoryVMRegistryLookupRejectsMismatchedStateDirectoryBeforeLockCreation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	repository, _, _ := repositoryIdentityFixture(t, root, "repository")
	identity, err := ResolveRepositoryIdentity(t.Context(), "operator", repository, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	canonicalDirectory := identity.StateDirectory
	if err := os.MkdirAll(canonicalDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	identity.StateDirectory = filepath.Join(stateRoot, "forged-state")
	if err := os.Mkdir(identity.StateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}

	registry, err := OpenRepositoryVMRegistry(stateRoot, newFakeRepositoryVMRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Lookup(t.Context(), identity); err == nil {
		t.Fatal("Lookup accepted a mismatched state directory")
	}
	for _, directory := range []string{canonicalDirectory, identity.StateDirectory} {
		if _, err := os.Stat(filepath.Join(directory, "registry.lock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("mismatched state directory created lock under %q: %v", directory, err)
		}
	}
}

func TestMicroVMMVP_Scenario2_RepositoryIdentityIsCanonicalAndConfined(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "owner state")
	repository, linked, symlink := repositoryIdentityFixtureWithLinkedWorktree(t, root, "hostile", "..", "same-name")
	other, _, _ := repositoryIdentityFixture(t, root, "other-parent", "same-name")

	identities := make([]RepositoryIdentity, 0, 3)
	for _, checkout := range []string{repository, linked, symlink} {
		identity, err := ResolveRepositoryIdentity(ctx, "../../operator\nwith/slashes", checkout, stateRoot)
		if err != nil {
			t.Fatalf("resolve hostile identity input: %v", err)
		}
		identities = append(identities, identity)
	}
	if identities[0] != identities[1] || identities[0] != identities[2] {
		t.Fatalf("linked/symlink identities diverged: %+v", identities)
	}
	otherIdentity, err := ResolveRepositoryIdentity(ctx, "../../operator\nwith/slashes", other, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if otherIdentity.Key == identities[0].Key {
		t.Fatalf("same repository display name collided across common directories: %+v %+v", identities[0], otherIdentity)
	}

	canonicalState, err := filepath.EvalSymlinks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range append(identities, otherIdentity) {
		relative, err := filepath.Rel(canonicalState, identity.StateDirectory)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			t.Fatalf("identity escaped owner state root: state=%q identity=%+v rel=%q err=%v", canonicalState, identity, relative, err)
		}
		for _, component := range strings.Split(relative, string(filepath.Separator)) {
			if component == "" || component == "." || component == ".." || strings.ContainsAny(component, "/\\\n") {
				t.Fatalf("unsafe identity path component %q in %q", component, relative)
			}
		}
	}
}

func TestRepositoryVMRegistryRejectsSymlinksAtEveryStateComponent(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"owners", "owner", "repositories", "repository", "registry.lock", "registry.json"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			stateRoot := filepath.Join(root, "state")
			repository, _, _ := repositoryIdentityFixture(t, root, "repository")
			identity, err := ResolveRepositoryIdentity(t.Context(), "operator", repository, stateRoot)
			if err != nil {
				t.Fatal(err)
			}
			ownerKey := framedDigest("mecatl.microvm.owner.v1", identity.Owner)
			components := []string{"owners", ownerKey, "repositories", identity.Key}
			index := map[string]int{"owners": 0, "owner": 1, "repositories": 2, "repository": 3}
			if componentIndex, ok := index[target]; ok {
				parent := stateRoot
				if componentIndex > 0 {
					parent = filepath.Join(append([]string{stateRoot}, components[:componentIndex]...)...)
					if err := os.MkdirAll(parent, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				external := filepath.Join(root, "external-directory")
				if err := os.Mkdir(external, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, filepath.Join(parent, components[componentIndex])); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(identity.StateDirectory, 0o700); err != nil {
					t.Fatal(err)
				}
				external := filepath.Join(root, "external-file")
				if err := os.WriteFile(external, []byte("unchanged"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, filepath.Join(identity.StateDirectory, target)); err != nil {
					t.Fatal(err)
				}
			}
			registry, err := OpenRepositoryVMRegistry(stateRoot, newFakeRepositoryVMRuntime())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Ensure(t.Context(), RepositoryVMRequest{Owner: identity.Owner, Checkout: repository, Artifacts: testArtifactSnapshot(repositoryVerifiedArtifacts(t, root))}); err == nil {
				t.Fatalf("Ensure followed %s symlink", target)
			}
			if target == "registry.lock" || target == "registry.json" {
				data, err := os.ReadFile(filepath.Join(root, "external-file"))
				if err != nil || string(data) != "unchanged" {
					t.Fatalf("external target changed: %q, %v", data, err)
				}
			}
		})
	}
}

func TestRepositoryVMRegistryMaterializesRootFSOnce(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	repository, linked, symlink := repositoryIdentityFixtureWithLinkedWorktree(t, root, "repository")
	verified := repositoryVerifiedArtifacts(t, root)
	runtime := newFakeRepositoryVMRuntime()
	registry, err := OpenRepositoryVMRegistry(stateRoot, runtime)
	if err != nil {
		t.Fatal(err)
	}

	first, err := registry.Ensure(ctx, RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: testArtifactSnapshot(verified)})
	if err != nil {
		t.Fatal(err)
	}
	injected, err := os.ReadFile(filepath.Join(first.Record.RootFSPath, guestAgentInstallPath))
	if err != nil || string(injected) != "guest-agent-complete" {
		t.Fatalf("injected independently verified guest agent = %q, %v", injected, err)
	}
	sharedMarker := filepath.Join(first.Record.RootFSPath, "home", "guest", "shared-package-marker")
	if err := os.WriteFile(sharedMarker, []byte("installed once"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, checkout := range []string{repository, linked, symlink, repository} {
		result, err := registry.Ensure(ctx, RepositoryVMRequest{Owner: "operator", Checkout: checkout})
		if err != nil {
			t.Fatalf("reuse repository generation: %v", err)
		}
		if result.Record.Generation != first.Record.Generation || result.Record.RootFSPath != first.Record.RootFSPath {
			t.Fatalf("reuse selected another rootfs: first=%+v got=%+v", first.Record, result.Record)
		}
	}
	marker, err := os.ReadFile(sharedMarker)
	if err != nil || string(marker) != "installed once" {
		t.Fatalf("subsequent ensure recopied rootfs: marker=%q err=%v", marker, err)
	}
	if got := runtime.startCount(); got != 1 {
		t.Fatalf("fake runtime starts = %d, want one", got)
	}
}

type artifactConsumingRuntime struct {
	*fakeRepositoryVMRuntime
	consume func(VerifiedArtifacts)
}

func (r *artifactConsumingRuntime) Start(ctx context.Context, record RepositoryVMRecord, artifacts VerifiedArtifacts, authority RepositoryBootAuthority) (RuntimeStatus, error) {
	r.consume(artifacts)
	return r.fakeRepositoryVMRuntime.Start(ctx, record, artifacts, authority)
}

type fakeRepositoryVMRuntime struct {
	mu          sync.Mutex
	starts      int
	aborts      int
	healthCalls int
	statuses    map[string]RuntimeStatus
	authorities map[string]RepositoryBootAuthority
	forgeHealth bool
}

func newFakeRepositoryVMRuntime() *fakeRepositoryVMRuntime {
	return &fakeRepositoryVMRuntime{statuses: make(map[string]RuntimeStatus), authorities: make(map[string]RepositoryBootAuthority)}
}

func (r *fakeRepositoryVMRuntime) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

func (r *fakeRepositoryVMRuntime) Start(_ context.Context, record RepositoryVMRecord, _ VerifiedArtifacts, authority RepositoryBootAuthority) (RuntimeStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	status := RuntimeStatus{Live: true, Generation: record.Boot.Generation, VMID: record.VMID, PID: 4000 + r.starts, ProcessIdentity: "boot-identity-" + record.VMID, Endpoint: record.Endpoint}
	r.statuses[record.VMID] = status
	r.authorities[record.VMID] = authority
	return status, nil
}

func (r *fakeRepositoryVMRuntime) Abort(_ context.Context, record RepositoryVMRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aborts++
	delete(r.statuses, record.VMID)
	delete(r.authorities, record.VMID)
	return nil
}

func (r *fakeRepositoryVMRuntime) Health(_ context.Context, record RepositoryVMRecord, challenge RepositoryHealthChallenge) (RepositoryHealthResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	status, ok := r.statuses[record.VMID]
	if !ok {
		return RepositoryHealthResponse{}, ErrEnvironmentUnavailable
	}
	if r.forgeHealth {
		return RepositoryHealthResponse{Status: status}, nil
	}
	response, err := r.authorities[record.VMID].HealthResponse(record, challenge, status)
	if err != nil {
		return RepositoryHealthResponse{}, err
	}
	r.healthCalls++
	return response, nil
}

func repositoryIdentityFixture(t *testing.T, root string, components ...string) (string, string, string) {
	t.Helper()
	repository := filepath.Join(append([]string{root}, components...)...)
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := gitexec.Run(ctx, repository, nil, "init"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexec.Run(ctx, repository, nil, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	identityEnv := []string{
		"GIT_AUTHOR_NAME=mecatl test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=mecatl test", "GIT_COMMITTER_EMAIL=test@example.invalid", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	}
	if _, err := gitexec.RunWithEnv(ctx, repository, nil, identityEnv, "commit", "-m", "fixture"); err != nil {
		t.Fatal(err)
	}
	return repository, "", ""
}

func repositoryIdentityFixtureWithLinkedWorktree(t *testing.T, root string, components ...string) (string, string, string) {
	t.Helper()
	repository, _, _ := repositoryIdentityFixture(t, root, components...)
	linked := repository + "-linked"
	if _, err := gitexec.Run(t.Context(), repository, nil, "worktree", "add", "--detach", linked); err != nil {
		t.Fatal(err)
	}
	symlink := repository + "-symlink"
	if err := os.Symlink(linked, symlink); err != nil {
		t.Fatal(err)
	}
	return repository, linked, symlink
}

func repositoryVerifiedArtifacts(t *testing.T, root string) VerifiedArtifacts {
	t.Helper()
	artifactSources := filepath.Join(root, "artifact-sources")
	if err := os.Mkdir(artifactSources, 0o700); err != nil {
		t.Fatal(err)
	}
	requests, resolver := testArtifactSet(t, artifactSources, nil, "")
	verified, _, err := NewProvisioner(
		NewVerifiedCache(filepath.Join(root, "verified-cache")),
		resolver,
		testPolicy("repository-policy-v1", nil),
		nil,
		nil,
	).Verify(t.Context(), requests)
	if err != nil {
		t.Fatalf("independently verify repository artifacts: %v", err)
	}
	return verified
}
