package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gomicrovmnet "github.com/stacklok/go-microvm/net"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/environment/microvm"
	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/control/controltest"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
	"github.com/stacklok/mecatl/internal/adapter/gitenv"
	microvmadapter "github.com/stacklok/mecatl/internal/adapter/microvm"
	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type multiBuildNetwork struct{ socket string }

func (*multiBuildNetwork) Start(context.Context, gomicrovmnet.Config) error { return nil }
func (n *multiBuildNetwork) SocketPath() string                             { return n.socket }
func (*multiBuildNetwork) Stop()                                            {}
func (*multiBuildNetwork) EgressDenials() uint64                            { return 0 }

type multiBuildGuestNetwork struct{}

func (*multiBuildGuestNetwork) DisableIPv6(context.Context) error { return nil }

type multiBuildVM struct{ status microvm.RuntimeStatus }

func (multiBuildVM) WaitReady(context.Context) error { return nil }
func (v multiBuildVM) Status(context.Context) (microvm.RuntimeStatus, error) {
	return v.status, nil
}
func (multiBuildVM) Stop(context.Context) error        { return nil }
func (multiBuildVM) CleanupBoot(context.Context) error { return nil }

type multiBuildBackend struct {
	mu     sync.Mutex
	server *guestagent.RepositoryServer
	starts int
	roots  map[string]string // offline mount translation, host path -> guest path
}

func (b *multiBuildBackend) Reconcile(context.Context, string) (microvm.LaunchReconcileResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.server = nil
	return microvm.LaunchReconcileResult{Dead: 1}, nil
}

func (b *multiBuildBackend) Start(_ context.Context, launch microvm.GoMicroVMLaunch) (microvm.GoMicroVMInstance, error) {
	if len(launch.Mounts) == 0 {
		return nil, errors.New("repository mount missing")
	}
	identity := guestexec.WorkloadIdentity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())} //nolint:gosec // test process identity
	contract := guestexec.DefaultRuntimeContract()
	contract.Identity = identity
	hostMount := launch.Mounts[0].HostPath
	guestServer, err := guestagent.NewRepositoryServer(guestagent.RepositoryServerConfig{
		Owner: launch.RepositoryOwner, RepositoryKey: launch.RepositoryKey, VMID: launch.VMID,
		Endpoint: launch.Endpoint, Generation: launch.Generation, PlacementGeneration: launch.PlacementGeneration,
		AuthorityKey: launch.CapabilityKey, WorkloadIdentity: identity, RuntimeContract: contract,
		ResolveRoot: func(guestRoot string) (string, error) {
			rel, err := filepath.Rel(microvm.RepositoryGuestMountRoot, guestRoot)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
				return "", errors.New("invalid guest root")
			}
			hostRoot := filepath.Join(hostMount, filepath.FromSlash(rel))
			b.mu.Lock()
			b.roots[hostRoot] = guestRoot
			b.mu.Unlock()
			return hostRoot, nil
		},
	})
	if err != nil {
		return nil, err
	}
	status := microvm.RuntimeStatus{Live: true, Generation: launch.Generation, VMID: launch.VMID, PID: 4242, ProcessIdentity: "offline-vm", Endpoint: launch.Endpoint}
	b.mu.Lock()
	b.server = guestServer
	b.starts++
	b.mu.Unlock()
	return multiBuildVM{status: status}, nil
}

func (b *multiBuildBackend) dial(ctx context.Context, _ string) (io.ReadWriteCloser, error) {
	b.mu.Lock()
	guestServer := b.server
	b.mu.Unlock()
	if guestServer == nil {
		return nil, errors.New("repository guest is not booted")
	}
	host, guest := net.Pipe()
	go func() {
		_ = guestServer.ServeAuthenticated(ctx, guest)
		_ = guest.Close()
	}()
	return host, nil
}

type multiBuildFixture struct {
	repository  string
	composition *microvm.RepositoryComposition
	backend     *multiBuildBackend
	verified    microvm.VerifiedArtifacts
	endpoint    string
}

func newMultiBuildFixture(t *testing.T) *multiBuildFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	poisonHooks := filepath.Join(root, "poison-hooks")
	for _, name := range []string{"pre-commit", "post-checkout"} {
		hook := filepath.Join(poisonHooks, name)
		mbWriteFile(t, hook, "#!/bin/sh\nprintf poison > \"$0.ran\"\nexit 71\n", 0o700)
		t.Cleanup(func() {
			if _, err := os.Stat(hook + ".ran"); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("ambient Git hook ran: %v", err)
			}
		})
	}
	poisonConfig := filepath.Join(os.Getenv("HOME"), ".gitconfig")
	mbWriteFile(t, poisonConfig, fmt.Sprintf("[core]\n hooksPath = %s\n[commit]\n gpgSign = true\n[gpg]\n program = /nonexistent-test-signer\n", poisonHooks), 0o600)
	t.Setenv("GIT_CONFIG_GLOBAL", poisonConfig)
	t.Setenv("GIT_DIR", filepath.Join(root, "not-the-repository.git"))
	t.Setenv("GIT_WORK_TREE", filepath.Join(root, "not-the-worktree"))
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "--initial-branch=main")
	mbWriteFile(t, filepath.Join(repository, "tracked.txt"), "base\n", 0o644)
	runGit(t, repository, "add", "tracked.txt")
	runGitEnv(t, repository, []string{"GIT_AUTHOR_NAME=mecatl test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=mecatl test", "GIT_COMMITTER_EMAIL=test@example.invalid"}, "commit", "-m", "fixture")

	artifacts := filepath.Join(root, "artifacts")
	verified := microvm.VerifiedArtifacts{}
	for _, kind := range []microvm.ArtifactKind{microvm.ArtifactRuntime, microvm.ArtifactFirmware, microvm.ArtifactExecutionImage, microvm.ArtifactGuestAgent} {
		path := filepath.Join(artifacts, string(kind))
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if kind == microvm.ArtifactGuestAgent {
			mbWriteFile(t, filepath.Join(path, "mecatl-guest-agent"), "offline guest agent", 0o700)
		}
		digest, err := microvm.ArtifactTreeDigest(path)
		if err != nil {
			t.Fatal(err)
		}
		artifact := microvm.VerifiedArtifact{Kind: kind, Digest: digest, Path: path}
		switch kind {
		case microvm.ArtifactRuntime:
			verified.Runtime = artifact
		case microvm.ArtifactFirmware:
			verified.Firmware = artifact
		case microvm.ArtifactExecutionImage:
			verified.ExecutionImage = artifact
		case microvm.ArtifactGuestAgent:
			verified.GuestAgent = artifact
		}
	}
	backend := &multiBuildBackend{roots: make(map[string]string)}
	network := microvm.NewNetworkController(func() gomicrovmnet.Provider { return &multiBuildNetwork{socket: filepath.Join(root, "network.sock")} }, &multiBuildGuestNetwork{})
	composition, err := microvm.NewRepositoryComposition(filepath.Join(root, "state"), microvm.RepositoryRuntimeConfig{
		Backend: backend, Network: network, GuestEgress: microvm.GuestEgressPolicy{Mode: microvm.EgressDenyAll}, DialGuest: backend.dial, DialControl: backend.dial,
	})
	if err != nil {
		t.Fatal(err)
	}

	socket := placementTestSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	auth, err := control.NewService(control.ServiceConfig{AccountUID: uint32(os.Getuid()), PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: uint32(os.Getuid())}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := microvm.NewRuntimeDaemon(microvm.RuntimeDaemonConfig{
		Control:    auth,
		Info:       microvm.DaemonInfo{ProtocolVersion: microvm.LifecycleProtocolVersion, ReleaseIdentity: "test", BinaryIdentity: "test", ConfigDigest: "test", PolicyRevision: "test", Profiles: []string{"microvm-local"}, Socket: socket},
		Repository: composition,
		RepositoryProvisioner: func(_ context.Context, request microvm.ProvisionRequest) (microvm.RepositoryPlacement, error) {
			return microvm.RepositoryPlacement{Request: microvm.LogicalEnvironmentRequest{Owner: request.Owner, Checkout: request.SourceCheckout, Artifacts: func(context.Context) (microvm.VerifiedArtifacts, func(), error) { return verified, func() {}, nil }}, Status: microvm.EnforcedProfileStatus{Profile: request.Profile, GuestEgress: "deny-all", HostEgress: "not constrained"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- daemon.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("microvmd stop: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("microvmd did not join retained handlers")
		}
	})
	return &multiBuildFixture{repository: repository, composition: composition, backend: backend, verified: verified, endpoint: "unix://" + socket}
}

func (f *multiBuildFixture) build(ctx context.Context, t *testing.T, checkout, storeDir string, turns ...mockllm.Turn) (*Built, *microvmadapter.Client, *[]port.LLMRequest) {
	t.Helper()
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	mbWriteFile(t, settings, "harness_context:\n  enabled_sources: [repository]\n  kinds:\n    instructions: {sources: [repository], mode: combine}\n    commands: {sources: [repository], mode: combine}\n    rules: {sources: [], mode: combine}\n    skills: {sources: [], mode: combine}\n    agent_defs: {sources: [], mode: combine}\npermissions:\n  allow: [Read, Shell]\n", 0o600)
	var requests []port.LLMRequest
	cfg, err := ConfigureExecution(Config{
		Workspace: checkout, StoreDir: storeDir, UserModelDir: t.TempDir(), MemoryDir: t.TempDir(), NoSoul: true, Shell: "/bin/sh",
		UseMock: true, MockProvider: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(request port.LLMRequest) { requests = append(requests, request) })}, turns...),
		TrustProject: true, OwnershipEnforced: true, PermissionConfigs: []string{settings}, DefaultPlacement: PlacementMicroVMLocal, DefaultPlacementSet: true,
		SchedulerEnabled: true, SchedulerTickInterval: time.Hour, SessionLeaseTTL: time.Second,
		MicroVMReadyRequest: func(microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, error) {
			return microvmmanager.ReadyRequest{}, nil
		},
		MicroVMManagerFactory: func() (MicroVMReadyManager, string, error) {
			return &placementReadyManager{endpoint: f.endpoint}, f.endpoint, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return built, cfg.PlacementProvider.(*microvmadapter.Client), &requests
}

func (*multiBuildFixture) placement(t *testing.T, client *microvmadapter.Client, sess *session.Session) microvmadapter.InventoryEntry {
	t.Helper()
	owner, err := json.Marshal([2]string{sess.Owner.Issuer, sess.Owner.Subject})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.Inventory(t.Context(), string(owner))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range page.Entries {
		if sess.EnvironmentRef.ID == entry.SessionID+"."+entry.EnvironmentID && sess.EnvironmentRef.Revision == fmt.Sprint(entry.Generation) {
			return entry
		}
	}
	t.Fatal("exact placement missing from daemon inventory")
	return microvmadapter.InventoryEntry{}
}

func TestMicroVMTwoBuildsSeparatePlacementsSurvivePeerClose(t *testing.T) {
	for _, linked := range []bool{false, true} {
		name := "same-checkout"
		if linked {
			name = "linked-host-worktrees"
		}
		t.Run(name, func(t *testing.T) {
			ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: "local-operator", GrantType: session.GrantTypeUser})
			fixture := newMultiBuildFixture(t)
			checkoutB := fixture.repository
			if linked {
				checkoutB = filepath.Join(filepath.Dir(fixture.repository), "linked")
				runGit(t, fixture.repository, "worktree", "add", "--detach", checkoutB)
			}
			mbWriteFile(t, filepath.Join(fixture.repository, "tracked.txt"), "HOST-A\n", 0o644)
			mbWriteFile(t, filepath.Join(fixture.repository, "AGENTS.md"), "SOURCE-A", 0o644)
			mbWriteFile(t, filepath.Join(fixture.repository, ".mecatl/commands/which.md"), "COMMAND-A", 0o644)

			turnsA := []mockllm.Turn{mockllm.TextTurn("a-context"), mockllm.ToolCallTurn(session.ToolCall{ID: "read-a", Name: "Read", Args: []byte(`{"path":"tracked.txt"}`)}), mockllm.TextTurn("a-after-close")}
			turnsB := []mockllm.Turn{mockllm.TextTurn("b-context"), mockllm.ToolCallTurn(session.ToolCall{ID: "shell-b", Name: "Shell", Args: []byte(`{"command":"cat tracked.txt"}`)}), mockllm.TextTurn("b-after-close")}
			storeDir := t.TempDir()
			first, firstClient, firstRequests := fixture.build(ctx, t, fixture.repository, storeDir, turnsA...)
			t.Cleanup(first.Close)
			one, err := first.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}

			mbWriteFile(t, filepath.Join(checkoutB, "tracked.txt"), "HOST-B\n", 0o644)
			mbWriteFile(t, filepath.Join(checkoutB, "AGENTS.md"), "SOURCE-B", 0o644)
			mbWriteFile(t, filepath.Join(checkoutB, ".mecatl/commands/which.md"), "COMMAND-B", 0o644)
			second, _, secondRequests := fixture.build(ctx, t, checkoutB, storeDir, turnsB...)
			t.Cleanup(second.Close)
			two, err := second.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if one.EnvironmentRef == two.EnvironmentRef {
				t.Fatal("independent Builds shared a logical ref")
			}
			if fixture.backend.starts != 1 {
				t.Fatalf("repository VM starts = %d, want 1", fixture.backend.starts)
			}

			for _, run := range []struct {
				built *Built
				id    session.SessionID
			}{{first, one.ID}, {second, two.ID}} {
				commands, err := run.built.Service.ListCommandsForSession(ctx, run.id)
				if err != nil || len(commands) != 1 || commands[0].Name != "which" {
					t.Fatalf("commands = %+v, %v", commands, err)
				}
				harnessRun(t, run.built, ctx, run.id, "/which")
			}
			assertRequestMarkers(t, (*firstRequests)[0], []string{"SOURCE-A", "COMMAND-A"}, []string{"SOURCE-B", "COMMAND-B"})
			assertRequestMarkers(t, (*secondRequests)[0], []string{"SOURCE-B", "COMMAND-B"}, []string{"SOURCE-A", "COMMAND-A"})

			if !linked {
				// Build B resolves and retires Build A's exact command source through the
				// shared service/store boundary; A's original execution owner must survive.
				borrowedCommands, err := second.Service.ListCommandsForSession(ctx, one.ID)
				if err != nil || len(borrowedCommands) != 1 || borrowedCommands[0].Name != "which" {
					t.Fatalf("cross-Build command reader = %+v, %v", borrowedCommands, err)
				}
				second.Service.CloseSession(one.ID)
				second.Close()
				placement := fixture.placement(t, firstClient, one)
				mbWriteFile(t, filepath.Join(placement.WorktreePath, "AGENTS.md"), "FRESH-A-INSTRUCTION", 0o644)
				mbWriteFile(t, filepath.Join(placement.WorktreePath, ".mecatl/commands/which.md"), "FRESH-A-COMMAND", 0o644)
				assertRunEvents(t, harnessRun(t, first, ctx, one.ID, "/which"), "read-a", "     1\tHOST-A\n")
				if len(*firstRequests) != 3 {
					t.Fatalf("A requests = %d, want initial plus post-reader tool and completion", len(*firstRequests))
				}
				assertRequestMarkers(t, (*firstRequests)[1], []string{"FRESH-A-INSTRUCTION", "FRESH-A-COMMAND"}, []string{"SOURCE-B", "COMMAND-B"})
				return
			}

			first.Service.CloseSession(one.ID)
			first.Close()
			assertRunEvents(t, harnessRun(t, second, ctx, two.ID, "use the execution tool"), "shell-b", "HOST-B\n[exit code: 0]")
			if len(*secondRequests) < 2 {
				t.Fatal("surviving Build made no post-close model request")
			}
			assertRequestMarkers(t, (*secondRequests)[1], []string{"SOURCE-B"}, []string{"SOURCE-A"})

			// B's command discovery remains usable after A shuts down.
			commands, err := second.Service.ListCommandsForSession(ctx, two.ID)
			if err != nil || len(commands) != 1 {
				t.Fatalf("post-close command borrow = %+v, %v", commands, err)
			}
			harnessRun(t, second, ctx, two.ID, "/which")
		})
	}
}

func TestMicroVMCrossBuildScheduleClosePreservesOrigin(t *testing.T) {
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: "local-operator", GrantType: session.GrantTypeUser})
	fixture := newMultiBuildFixture(t)
	storeDir := t.TempDir()
	leader, _, leaderRequests := fixture.build(ctx, t, fixture.repository, storeDir, mockllm.TextTurn("fire complete"))
	t.Cleanup(leader.Close)
	originBuild, originClient, originRequests := fixture.build(ctx, t, fixture.repository, storeDir,
		mockllm.ToolCallTurn(session.ToolCall{ID: "origin-read", Name: "Read", Args: []byte(`{"path":"tracked.txt"}`)}),
		mockllm.TextTurn("origin still complete"))
	t.Cleanup(originBuild.Close)

	mbWriteFile(t, filepath.Join(fixture.repository, "tracked.txt"), "ORIGIN-FILE\n", 0o644)
	mbWriteFile(t, filepath.Join(fixture.repository, "AGENTS.md"), "ORIGIN-INSTRUCTION", 0o644)
	mbWriteFile(t, filepath.Join(fixture.repository, ".mecatl/commands/which.md"), "ORIGIN-COMMAND", 0o644)
	origin, err := originBuild.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := originBuild.Service.CreateSchedule(ctx, port.ScheduleSpec{
		Name: "cross-build", Prompt: "Read tracked.txt and report its contents; do not mutate files.",
		Trigger: port.TriggerSpec{Cron: "0 0 * * *"}, OriginSessionID: origin.ID, Mode: session.ModePlan, Mutating: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if schedule.Spec.EnvironmentRef != origin.EnvironmentRef || schedule.Spec.PlacementOwned {
		t.Fatalf("origin-backed schedule placement = %+v", schedule.Spec)
	}
	fire, err := leader.Service.FireNow(ctx, schedule.Spec.Name)
	if err != nil || fire.SessionID == "" || fire.Stop == session.StopError {
		t.Fatalf("leader FireNow = %+v, %v", fire, err)
	}
	if len(*leaderRequests) == 0 {
		t.Fatal("leader made no model request for completed fire")
	}
	foundOrigin := false
	for _, request := range *leaderRequests {
		var text strings.Builder
		for _, message := range request.Messages {
			text.WriteString(message.Text)
		}
		foundOrigin = foundOrigin || strings.Contains(text.String(), "ORIGIN-INSTRUCTION")
	}
	if !foundOrigin {
		t.Fatal("leader fire model requests omitted origin source marker")
	}
	leader.Close()

	// FireNow is synchronous through the elected leader: its deferred CloseSession has
	// run before this uses B's already-acquired original Service binding.
	assertRunEvents(t, harnessRun(t, originBuild, ctx, origin.ID, "read from the original environment"), "origin-read", "     1\tORIGIN-FILE\n")
	if len(*originRequests) != 2 {
		t.Fatalf("origin model requests = %d, want tool request and completion", len(*originRequests))
	}
	for _, request := range *originRequests {
		assertRequestMarkers(t, request, []string{"ORIGIN-INSTRUCTION"}, nil)
	}
	placement := fixture.placement(t, originClient, origin)
	fixture.backend.mu.Lock()
	guest := fixture.backend.server
	guestRoot := fixture.backend.roots[placement.WorktreePath]
	fixture.backend.mu.Unlock()
	logicalID := strings.TrimPrefix(placement.EnvironmentID, "logical-")
	guestBinding := control.Binding{Owner: placement.Owner, SessionID: logicalID, EnvironmentID: logicalID, Ref: placement.Ref, Generation: placement.Generation, AssignedRoot: guestRoot}
	if err := guest.Probe(guestBinding); err != nil {
		t.Fatalf("positive control: origin guest registration unavailable before final close: %v", err)
	}
	originBuild.Service.CloseSession(origin.ID)
	originBuild.Close()
	if err := guest.Probe(guestBinding); !errors.Is(err, control.ErrBindingMismatch) {
		t.Fatalf("final owner close retained logical guest registration: %v", err)
	}
	if _, err := originClient.DaemonInfo(t.Context()); err != nil {
		t.Fatalf("registration disappeared because daemon stopped, not final owner release: %v", err)
	}
	if _, err := os.Stat(placement.WorktreePath); err != nil {
		t.Fatalf("final release destroyed durable worktree: %v", err)
	}
	leader.Close()
	if fixture.backend.starts != 1 {
		t.Fatalf("repository VM starts = %d, want one shared VM", fixture.backend.starts)
	}
}

func assertRunEvents(t *testing.T, events []session.Event, callID session.ToolCallID, want string) {
	t.Helper()
	var result *session.ResultPayload
	matches := 0
	for _, event := range events {
		if event.ToolResult != nil && event.ToolResult.CallID == callID {
			matches++
			if event.ToolResult.Content != want {
				t.Fatalf("tool %s content = %q, want %q", callID, event.ToolResult.Content, want)
			}
		}
		if event.ToolResult != nil && event.ToolResult.IsError {
			t.Fatalf("tool %s failed: %s", event.ToolResult.CallID, event.ToolResult.Content)
		}
		if event.Result != nil {
			result = event.Result
		}
	}
	if matches != 1 {
		t.Fatalf("tool %s result count = %d, want exactly one", callID, matches)
	}
	if result == nil || result.Stop == session.StopError {
		t.Fatalf("run result = %+v", result)
	}
}

func assertRequestMarkers(t *testing.T, request port.LLMRequest, present, absent []string) {
	t.Helper()
	var text strings.Builder
	for _, message := range request.Messages {
		text.WriteString(message.Text)
		text.WriteByte('\n')
	}
	for _, marker := range present {
		if !strings.Contains(text.String(), marker) {
			t.Fatalf("model request omitted %q", marker)
		}
	}
	for _, marker := range absent {
		if strings.Contains(text.String(), marker) {
			t.Fatalf("model request crossed poison marker %q", marker)
		}
	}
}

func mbWriteFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) { runGitEnv(t, dir, nil, args...) }
func runGitEnv(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitenv.Scrub(os.Environ()), env...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

// Keep compile-time coverage of the service's placement scope used by the real adapter.
var _ server.PlacementScope = "deployment"
