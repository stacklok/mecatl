package microvm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/gitexec"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

func TestRepositoryLogicalStageFailureIsClosedAndSecretFree(t *testing.T) {
	const secret = "token=repository-secret /private/worktree errno=13"
	err := repositoryLogicalFailure(repositoryLogicalStagePrepare, errors.New(secret))
	response := lifecycleFailure(err)
	if response.ErrorCode != "failed_precondition" || response.ErrorText != "microvm repository logical environment failed during prepare" {
		t.Fatalf("response = %+v", response)
	}
	if !errors.Is(response.Err, errors.Unwrap(err)) || strings.Contains(response.ErrorText, secret) || strings.Contains(response.ErrorText, "/private/worktree") {
		t.Fatalf("lifecycle response leaked backend detail or lost cause: %+v", response)
	}
}

func TestRepositoryLogicalRootUnavailableLifecycleFailureIsClosed(t *testing.T) {
	const secret = "token=repository-secret /private/worktree errno=13"
	response := lifecycleFailure(fmt.Errorf("%w: %s", ErrRepositoryLogicalRootUnavailable, secret))
	if response.ErrorCode != "repository_logical_root_unavailable" || response.ErrorText != ErrRepositoryLogicalRootUnavailable.Error() ||
		!errors.Is(response.Err, ErrRepositoryLogicalRootUnavailable) {
		t.Fatalf("response = %+v", response)
	}
	if strings.Contains(response.ErrorText, secret) || strings.Contains(response.ErrorText, "/private/worktree") {
		t.Fatalf("lifecycle response leaked backend detail: %+v", response)
	}
}

func TestRepositoryLogicalManagerRegisterFailureIsClosedAndClassified(t *testing.T) {
	fixture := newLogicalRepositoryFixture(t)
	const secret = "control transport failed: token=repository-secret /private/worktree"
	manager, err := NewRepositoryLogicalManager(fixture.manager.registry, fixture.manager.preparer, failingRepositoryGuest{err: errors.New(secret)})
	if err != nil {
		t.Fatal(err)
	}
	request := LogicalEnvironmentRequest{Owner: "operator", Checkout: fixture.repo, Verified: fixture.verified}

	_, err = manager.Create(t.Context(), request)
	assertLogicalRootUnavailable(t, err, secret)
	response := lifecycleFailure(err)
	if response.ErrorCode != "repository_logical_root_unavailable" || response.ErrorText != ErrRepositoryLogicalRootUnavailable.Error() {
		t.Fatalf("daemon lifecycle response = %+v", response)
	}

	environment := fixture.create(t)
	defer environment.Close()
	_, err = manager.Reattach(t.Context(), request, environment.Ref)
	assertLogicalRootUnavailable(t, err, secret)
	response = lifecycleFailure(err)
	if response.ErrorCode != "repository_logical_root_unavailable" || response.ErrorText != ErrRepositoryLogicalRootUnavailable.Error() {
		t.Fatalf("daemon reattach lifecycle response = %+v", response)
	}
}

func assertLogicalRootUnavailable(t *testing.T, err error, secret string) {
	t.Helper()
	if !errors.Is(err, ErrRepositoryLogicalRootUnavailable) {
		t.Fatalf("error = %v, want logical root unavailable", err)
	}
	if unwrapped := errors.Unwrap(err); unwrapped != ErrRepositoryLogicalRootUnavailable || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "/private/worktree") {
		t.Fatalf("error exposed guest registration detail or lost canonical cause: %v", err)
	}
}

func TestMicroVMMVP_Scenario3_LogicalEnvironmentsShareVMNotWorktree(t *testing.T) {
	t.Parallel()
	fixture := newLogicalRepositoryFixture(t)

	first := fixture.create(t)
	second := fixture.create(t)
	defer first.Close()
	defer second.Close()

	if first.Repository.Generation != second.Repository.Generation || first.Repository.VMID != second.Repository.VMID {
		t.Fatalf("logical environments did not share repository VM: first=%+v second=%+v", first.Repository, second.Repository)
	}
	if first.Binding.Ref == second.Binding.Ref || first.Binding.AssignedRoot == second.Binding.AssignedRoot ||
		first.WorktreePath == second.WorktreePath || first.Branch == second.Branch || first.IndexPath == second.IndexPath {
		t.Fatalf("logical environments shared identity or Git state: first=%+v second=%+v", first, second)
	}
	for _, environment := range []*LogicalEnvironment{first, second} {
		if environment.Binding.Generation != environment.Repository.Generation || environment.Binding.Owner != "operator" {
			t.Fatalf("logical binding drifted from repository VM: %+v", environment)
		}
		if _, err := os.Stat(environment.IndexPath); err != nil {
			t.Fatalf("logical environment has no distinct Git index %q: %v", environment.IndexPath, err)
		}
	}
	if got := fixture.runtime.startCount(); got != 1 {
		t.Fatalf("repository VM starts = %d, want 1", got)
	}
}

func TestMicroVMMVP_Scenario3_EnvironmentRefAuthenticatesAssignedRoot(t *testing.T) {
	t.Parallel()
	fixture := newLogicalRepositoryFixture(t)
	environment := fixture.create(t)
	defer environment.Close()

	if got, err := environment.Workspace.Read(t.Context(), "tracked.txt"); err != nil || string(got) != "source\n" {
		t.Fatalf("authenticated workspace read = %q, %v", got, err)
	}
	result, err := environment.Runner.Run(t.Context(), "pwd; cat tracked.txt")
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout, environment.WorktreePath+"\nsource\n") {
		t.Fatalf("authenticated exec = %+v, %v", result, err)
	}

	mutations := []func(control.Binding) control.Binding{
		func(binding control.Binding) control.Binding { binding.Owner = "other"; return binding },
		func(binding control.Binding) control.Binding { binding.Generation++; return binding },
		func(binding control.Binding) control.Binding {
			binding.Ref = "microvm-local:logical-sibling"
			return binding
		},
		func(binding control.Binding) control.Binding { binding.AssignedRoot += "-sibling"; return binding },
	}
	for _, mutate := range mutations {
		claim := mutate(environment.Binding)
		if err := fixture.guest.Probe(t.Context(), environment.Repository, claim); !errors.Is(err, control.ErrBindingMismatch) {
			t.Fatalf("forged binding %+v probe = %v, want binding mismatch", claim, err)
		}
	}

	fixture.guest.mu.Lock()
	server := fixture.guest.servers[environment.Repository.VMID]
	issuer := fixture.guest.issuers[environment.Repository.VMID]
	fixture.guest.mu.Unlock()
	wrongRegistration := environment.Binding
	wrongRegistration.AssignedRoot += "-sibling"
	registration, err := issuer.Issue(wrongRegistration)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := issuer.Issue(environment.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Register(t.Context(), registration, wrongRegistration); !errors.Is(err, guestagent.ErrLogicalRootUnavailable) {
		t.Fatalf("registration with unavailable assigned root = %v, want logical root unavailable", err)
	}
	wrongRoot := environment.Binding
	wrongRoot.AssignedRoot += "-sibling"
	services, err := connectLogicalGuest(t.Context(), server, wrongRoot, capability)
	if services != nil {
		_ = services.Close()
	}
	if !errors.Is(err, control.ErrUnauthenticatedCapability) {
		t.Fatalf("handshake accepted wrong assigned root: %v", err)
	}
}

func TestInvariant_microvm_logical_environment_is_confined_and_affined(t *testing.T) {
	t.Parallel()
	fixture := newLogicalRepositoryFixture(t)
	first := fixture.create(t)
	second := fixture.create(t)
	defer first.Close()
	defer second.Close()

	if _, err := first.Workspace.Read(t.Context(), "../"+filepath.Base(second.WorktreePath)+"/tracked.txt"); err == nil {
		t.Fatal("workspace path escape reached sibling worktree")
	}
	if _, err := first.Workspace.Read(t.Context(), second.WorktreePath+"/tracked.txt"); err == nil {
		t.Fatal("absolute cross-worktree read succeeded")
	}
	if _, err := second.Workspace.CreateFile(t.Context(), "only-second", []byte("sibling")); err != nil {
		t.Fatal(err)
	}
	result, err := first.Runner.Run(t.Context(), "test \"$PWD\" = "+shellQuote(first.WorktreePath)+" && cat "+shellQuote(second.WorktreePath+"/only-second"))
	if err != nil || result.ExitCode != 0 || result.Stdout != "sibling" {
		t.Fatalf("same-repository Bash could not address sibling guest path: %+v, %v", result, err)
	}
	if _, err := first.Workspace.Read(t.Context(), "only-second"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first workspace observed sibling bytes: %v", err)
	}

	_, version, err := first.Workspace.ReadVersion(t.Context(), "tracked.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Workspace.ReplaceFile(t.Context(), "tracked.txt", version, []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Workspace.ReplaceFile(t.Context(), "tracked.txt", version, []byte("replay\n")); err == nil {
		t.Fatal("replayed file version overwrote newer assigned-root bytes")
	}
	got, err := os.ReadFile(filepath.Join(second.WorktreePath, "tracked.txt"))
	if err != nil || string(got) != "source\n" {
		t.Fatalf("first mutation crossed into sibling worktree: %q, %v", got, err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := first.Runner.Run(ctx, "sleep 5"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("guest cancellation = %v, want deadline exceeded", err)
	}

	stale := first.Binding
	stale.Generation++
	if err := fixture.guest.Probe(t.Context(), first.Repository, stale); !errors.Is(err, control.ErrBindingMismatch) {
		t.Fatalf("stale request = %v, want fail closed", err)
	}
	if err := fixture.guest.Replay(t.Context(), first.Repository, first.Binding); !errors.Is(err, control.ErrUnauthenticatedCapability) {
		t.Fatalf("replayed capability = %v, want unauthenticated", err)
	}
}

func TestLogicalEnvironmentCloseRevokesGuestRegistrationBeforeCleanup(t *testing.T) {
	t.Parallel()
	fixture := newLogicalRepositoryFixture(t)
	environment := fixture.create(t)
	binding := environment.Binding
	repository := environment.Repository
	worktreePath := environment.WorktreePath

	if err := environment.Close(); err != nil {
		t.Fatalf("close logical environment: %v", err)
	}
	if err := fixture.guest.Probe(t.Context(), repository, binding); !errors.Is(err, control.ErrBindingMismatch) {
		t.Fatalf("closed binding probe = %v, want binding mismatch", err)
	}
	fixture.guest.mu.Lock()
	server, issuer := fixture.guest.servers[repository.VMID], fixture.guest.issuers[repository.VMID]
	fixture.guest.mu.Unlock()
	freshCapability, err := issuer.Issue(binding)
	if err != nil {
		t.Fatal(err)
	}
	if services, err := connectLogicalGuest(t.Context(), server, binding, freshCapability); !errors.Is(err, control.ErrUnauthenticatedCapability) {
		if services != nil {
			_ = services.Close()
		}
		t.Fatalf("fresh capability reopened closed ref: %v", err)
	}
	if _, err := environment.Workspace.Read(t.Context(), "tracked.txt"); err == nil {
		t.Fatal("closed workspace still dispatched")
	}
	if _, err := environment.Runner.Run(t.Context(), "true"); err == nil {
		t.Fatal("closed runner still dispatched")
	}
	if _, err := os.Stat(worktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed logical worktree still exists: %v", err)
	}
	if err := environment.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	sibling := fixture.create(t)
	defer sibling.Close()
	if _, err := sibling.Workspace.Read(t.Context(), "tracked.txt"); err != nil {
		t.Fatalf("closing one ref harmed shared VM sibling: %v", err)
	}
}

func TestMicroVMMVP_Scenario4_RepositoryGenerationOwnsSingleRootFS(t *testing.T) {
	t.Parallel()
	fixture := newLogicalRepositoryFixture(t)
	first := fixture.create(t)
	second := fixture.create(t)
	defer first.Close()
	defer second.Close()

	if first.Repository.RootFSPath != second.Repository.RootFSPath {
		t.Fatalf("logical environments got different rootfs paths: %q %q", first.Repository.RootFSPath, second.Repository.RootFSPath)
	}
	if got := fixture.runtime.startCount(); got != 1 {
		t.Fatalf("repository generation materializations/starts = %d, want 1", got)
	}
	fixture.runtime.mu.Lock()
	healthCalls := fixture.runtime.healthCalls
	fixture.runtime.mu.Unlock()
	if healthCalls == 0 {
		t.Fatal("logical environment creation did not authenticate repository-generation health")
	}
	guestBytes, err := os.ReadFile(filepath.Join(first.Repository.RootFSPath, guestAgentInstallPath))
	if err != nil || string(guestBytes) != "guest-agent-complete" {
		t.Fatalf("generation rootfs guest agent = %q, %v", guestBytes, err)
	}
	if _, err := os.Stat(filepath.Join(first.Repository.RootFSPath, strings.TrimPrefix(GuestPrebootConfigPath, "/"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository rootfs contains session-bound preboot config: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(first.Repository.RootFSPath), "rootfs*")); err != nil || len(matches) != 1 {
		t.Fatalf("session creation cloned repository rootfs: matches=%v err=%v", matches, err)
	}
}

type logicalRepositoryFixture struct {
	t        *testing.T
	repo     string
	verified VerifiedArtifacts
	runtime  *fakeRepositoryVMRuntime
	guest    *testRepositoryGuest
	manager  *RepositoryLogicalManager
}

type failingRepositoryGuest struct{ err error }

func (g failingRepositoryGuest) Register(context.Context, RepositoryVMRecord, control.Binding, RepositoryGuestMount) (*guestagent.Services, error) {
	return nil, g.err
}

func (failingRepositoryGuest) Unregister(context.Context, RepositoryVMRecord, control.Binding) error {
	return nil
}

func newLogicalRepositoryFixture(t *testing.T) *logicalRepositoryFixture {
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
	if _, err := gitexecForLogicalTest(t.Context(), repository, "commit", "-m", "fixture"); err != nil {
		t.Fatal(err)
	}
	runtime := newFakeRepositoryVMRuntime()
	registry, err := OpenRepositoryVMRegistry(filepath.Join(root, "state"), runtime)
	if err != nil {
		t.Fatal(err)
	}
	guest := newTestRepositoryGuest(t)
	manager, err := NewRepositoryLogicalManager(registry, worktree.New(), guest)
	if err != nil {
		t.Fatal(err)
	}
	return &logicalRepositoryFixture{t: t, repo: repository, verified: repositoryVerifiedArtifacts(t, root), runtime: runtime, guest: guest, manager: manager}
}

func (f *logicalRepositoryFixture) create(t *testing.T) *LogicalEnvironment {
	t.Helper()
	environment, err := f.manager.Create(t.Context(), LogicalEnvironmentRequest{Owner: "operator", Checkout: f.repo, Verified: f.verified})
	if err != nil {
		t.Fatalf("create logical environment: %v", err)
	}
	return environment
}

type testRepositoryGuest struct {
	mu      sync.Mutex
	servers map[string]*guestagent.RepositoryServer
	issuers map[string]*control.CapabilityIssuer
	tokens  map[string]string
	mounts  map[string]string
}

func newTestRepositoryGuest(*testing.T) *testRepositoryGuest {
	return &testRepositoryGuest{servers: make(map[string]*guestagent.RepositoryServer), issuers: make(map[string]*control.CapabilityIssuer), tokens: make(map[string]string), mounts: make(map[string]string)}
}

func (g *testRepositoryGuest) Register(ctx context.Context, record RepositoryVMRecord, binding control.Binding, mount RepositoryGuestMount) (*guestagent.Services, error) {
	g.mu.Lock()
	g.mounts[mount.GuestPath] = mount.HostPath
	server := g.servers[record.VMID]
	if server == nil {
		key := []byte("0123456789abcdef0123456789abcdef")
		identity := guestexec.WorkloadIdentity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())} //nolint:gosec // test process identity
		contract := guestexec.DefaultRuntimeContract()
		contract.Identity = identity
		var err error
		server, err = guestagent.NewRepositoryServer(guestagent.RepositoryServerConfig{
			Owner: record.Owner, Generation: record.Generation, AuthorityKey: key,
			WorkloadIdentity: identity, RuntimeContract: contract,
			ResolveRoot: func(guestRoot string) (string, error) {
				root := g.mounts[guestRoot]
				if root == "" {
					return "", errors.New("guest mount is absent")
				}
				return root, nil
			},
		})
		if err != nil {
			g.mu.Unlock()
			return nil, err
		}
		issuer, err := control.NewCapabilityIssuer(key)
		if err != nil {
			g.mu.Unlock()
			return nil, err
		}
		g.servers[record.VMID] = server
		g.issuers[record.VMID] = issuer
	}
	issuer := g.issuers[record.VMID]
	registration, err := issuer.Issue(binding)
	var capability string
	if err == nil {
		capability, err = issuer.Issue(binding)
	}
	if err == nil {
		err = server.Register(ctx, registration, binding)
	}
	g.tokens[binding.Ref] = capability
	g.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return connectLogicalGuest(ctx, server, binding, capability)
}

func (g *testRepositoryGuest) Unregister(_ context.Context, record RepositoryVMRecord, binding control.Binding) error {
	g.mu.Lock()
	server, issuer := g.servers[record.VMID], g.issuers[record.VMID]
	if server == nil || issuer == nil {
		g.mu.Unlock()
		return errors.New("guest not started")
	}
	authority, err := issuer.Issue(binding)
	if err == nil {
		err = server.Unregister(authority, binding)
	}
	delete(g.tokens, binding.Ref)
	delete(g.mounts, binding.AssignedRoot)
	g.mu.Unlock()
	return err
}

func (g *testRepositoryGuest) Probe(_ context.Context, record RepositoryVMRecord, binding control.Binding) error {
	g.mu.Lock()
	server := g.servers[record.VMID]
	g.mu.Unlock()
	if server == nil {
		return errors.New("guest not started")
	}
	return server.Probe(binding)
}

func (g *testRepositoryGuest) Replay(ctx context.Context, record RepositoryVMRecord, binding control.Binding) error {
	g.mu.Lock()
	server, capability := g.servers[record.VMID], g.tokens[binding.Ref]
	g.mu.Unlock()
	services, err := connectLogicalGuest(ctx, server, binding, capability)
	if services != nil {
		_ = services.Close()
	}
	return err
}

func connectLogicalGuest(ctx context.Context, server *guestagent.RepositoryServer, binding control.Binding, capability string) (*guestagent.Services, error) {
	host, guest := net.Pipe()
	go func() { _ = server.Serve(ctx, guest) }()
	services, err := guestagent.Connect(ctx, host, binding, capability)
	if err != nil {
		_ = host.Close()
	}
	return services, err
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func gitexecForLogicalTest(ctx context.Context, root string, args ...string) ([]byte, error) {
	return gitexec.RunWithEnv(ctx, root, nil, []string{"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid"}, args...)
}
