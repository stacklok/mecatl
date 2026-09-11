package microvm

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	gomicrovmnet "github.com/stacklok/go-microvm/net"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/control/controltest"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
	"github.com/stacklok/mecatl/environment/microvm/virtiofs"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

func TestMicroVMMVP_Scenario6_LinuxUserNamespaceAvoidsWorldModeWidening(t *testing.T) {
	if goruntime.GOOS != "linux" || goruntime.GOARCH != "amd64" {
		t.Skip("Linux amd64 user-namespace contract")
	}

	backend := NewLibkrunBackend()
	workload := guestexec.DefaultWorkloadIdentity()
	if workload.UID != 65532 || workload.GID != 65532 || backend.userNamespaceUID != int(workload.UID) || backend.userNamespaceGID != int(workload.GID) {
		t.Fatalf("libkrun workload user namespace = %d:%d for workload %d:%d, want 65532:65532", backend.userNamespaceUID, backend.userNamespaceGID, workload.UID, workload.GID)
	}
	if workload.UID == 0 || workload.GID == 0 {
		t.Fatal("model workload identity is privileged")
	}

	prepared := preparedRuntimeWorktree(t, t.TempDir())
	privateFile := filepath.Join(prepared.WorktreePath, "private")
	if err := os.WriteFile(privateFile, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := virtiofs.Plan(prepared, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.PrepareWorkloadAccess(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		prepared.WorktreePath: 0o700,
		prepared.MetadataPath: 0o700,
		privateFile:           0o600,
	} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if got := info.Mode().Perm(); got != want || got&0o007 != 0 {
			t.Errorf("mode %s = %#o, want private %#o", path, got, want)
		}
	}
}

func TestLibkrunGenerationOwnsRuntimeArtifacts(t *testing.T) {
	root := t.TempDir()
	runtimeSource := filepath.Join(root, "verified-runtime")
	firmwareSource := filepath.Join(root, "verified-firmware")
	for path, content := range map[string]string{runtimeSource: "runner", firmwareSource: "firmware"} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "payload"), []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	runtimePath, firmwarePath, ownedRoot, err := prepareOwnedRuntimeArtifacts(runtimeSource, firmwareSource, root, "generation")
	if err != nil {
		t.Fatalf("prepare owned runtime artifacts: %v", err)
	}
	defer func() { _ = os.RemoveAll(ownedRoot) }()
	if err := os.RemoveAll(runtimeSource); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(firmwareSource); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{runtimePath: "runner", firmwarePath: "firmware"} {
		got, readErr := os.ReadFile(filepath.Join(path, "payload"))
		if readErr != nil || string(got) != want {
			t.Fatalf("owned artifact %s after launch snapshot release = %q, %v", path, got, readErr)
		}
	}
}

func TestMicroVMRuntime_FreshInstanceVerifiesExactRuntimeIdentity(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	prepared := preparedRuntimeWorktree(t, root)
	binding := control.Binding{Owner: "caller:alice", SessionID: "session-runtime", EnvironmentID: "env-runtime", Ref: "env-runtime@3", Generation: 3}
	backend := newRuntimeTestBackend(t)
	runtime, err := NewGoMicroVMRuntime(GoMicroVMRuntimeConfig{
		Backend: backend, Network: NewNetworkController(func() gomicrovmnet.Provider { return &runtimeTestNetwork{socket: filepath.Join(root, "network.sock")} }, prebootIPv6Only{}),
		GuestEgress: GuestEgressPolicy{Mode: EgressDenyAll}, DialGuest: backend.DialGuest,
		CapabilityKey: func() ([]byte, error) { return []byte("0123456789abcdef0123456789abcdef"), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	createRequest := runtimeCreateRequest(prepared, binding)
	verified, _, err := runtimeArtifactVerifier(root).Verify(ctx, createRequest.ArtifactRequests)
	if err != nil {
		t.Fatal(err)
	}
	request := VMCreateRequest{EnvironmentID: binding.EnvironmentID, VMID: "vm-runtime", Endpoint: filepath.Join(root, "guest.sock"), Generation: binding.Generation, Owner: binding.Owner, SessionID: binding.SessionID, Prepared: prepared, Verified: verified, Preboot: GuestPrebootArtifact{RootfsPath: GuestPrebootConfigPath, Config: GuestPrebootConfig{DisableIPv6: true, AgentEndpoint: filepath.Join(root, "guest.sock"), Binding: binding, Capabilities: control.RequiredCapabilities(), MaxMessageBytes: control.DefaultMaxMessageBytes}}}
	if err := runtime.Create(ctx, request); err != nil {
		t.Fatalf("Create: %v", err)
	}
	status, err := runtime.Inspect(ctx, EnvironmentRecord{Ref: EnvironmentRef{Kind: Kind, ID: binding.Ref}})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	record := EnvironmentRecord{State: EnvironmentReady, Owner: binding.Owner, SessionID: binding.SessionID, EnvironmentID: binding.EnvironmentID, Ref: EnvironmentRef{Kind: Kind, ID: binding.Ref}, Generation: binding.Generation, VMID: status.VMID, RunnerPID: status.PID, ProcessIdentity: status.ProcessIdentity, Endpoint: status.Endpoint, Agreement: control.Agreement{Version: control.ProtocolVersion, Capabilities: control.RequiredCapabilities(), MaxMessageBytes: control.DefaultMaxMessageBytes}}

	fresh, err := NewGoMicroVMRuntime(GoMicroVMRuntimeConfig{Backend: backend, Network: runtime.network, GuestEgress: runtime.guestEgress, DialGuest: backend.DialGuest, CapabilityKey: runtime.capabilityKey})
	if err != nil {
		t.Fatal(err)
	}
	wrong := record
	wrong.Endpoint += ".stale"
	if err := fresh.Reattach(ctx, wrong); !errors.Is(err, ErrRuntimeIdentityMismatch) {
		t.Fatalf("Reattach(stale endpoint) = %v, want identity mismatch", err)
	}
}

func TestLibkrunBackend_FreshBackendOpensDestructionOnlyExactProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.Command("sleep", "30")
	if err := command.Start(); err != nil {
		t.Skipf("sleep helper unavailable: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	defer func() {
		_ = command.Process.Kill()
		select {
		case <-waited:
		case <-time.After(time.Second):
		}
	}()

	root := t.TempDir()
	t.Chdir(root)
	endpoint := "guest.sock"
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	identity, err := runnerProcessIdentity(ctx, command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	record := readyRecord("fresh-backend", 9)
	record.RunnerPID = command.Process.Pid
	record.ProcessIdentity = identity
	record.Endpoint = endpoint

	instance, err := NewLibkrunBackend().Open(ctx, record)
	if err != nil {
		t.Fatalf("fresh backend Open(): %v", err)
	}
	status, err := instance.Status(ctx)
	if err != nil || status.ProcessIdentity != identity || status.Generation != record.Generation {
		t.Fatalf("fresh backend status = %+v, %v", status, err)
	}
	if err := instance.Stop(ctx); err != nil {
		t.Fatalf("identity-checked orphan Stop(): %v", err)
	}
	if err := instance.Remove(ctx); err != nil {
		t.Fatalf("orphan Remove(): %v", err)
	}
	select {
	case <-waited:
	case <-ctx.Done():
		t.Fatal("orphan runner remained live after destruction")
	}
}

func TestMicroVMRuntime_ComposesVerifiedEnvironmentEndToEnd(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	root := t.TempDir()
	prepared := preparedRuntimeWorktree(t, root)
	endpoint := filepath.Join(root, "guest.sock")
	identity := EnvironmentIdentity{EnvironmentID: "env-runtime", VMID: "vm-runtime", Endpoint: endpoint, Generation: 3}
	binding := control.Binding{Owner: "caller:alice", SessionID: "session-runtime", EnvironmentID: identity.EnvironmentID, Ref: "env-runtime@3", Generation: identity.Generation}

	provider := &runtimeTestNetwork{socket: filepath.Join(root, "network.sock")}
	observer := NewOperationsObserver(nil)
	backend := newRuntimeTestBackend(t)
	registry := newLifecycleRegistry()
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	rootComposition, err := NewRuntimeDaemon(RuntimeDaemonConfig{
		Control: auth, Backend: backend,
		Network:     NewNetworkController(func() gomicrovmnet.Provider { return provider }, prebootIPv6Only{}),
		GuestEgress: GuestEgressPolicy{Mode: EgressDenyAll}, DialGuest: backend.DialGuest,
		CapabilityKey: func() ([]byte, error) { return []byte("0123456789abcdef0123456789abcdef"), nil },
		Identities:    staticRuntimeIdentity{identity}, Worktrees: staticRuntimeWorktree{prepared: prepared},
		Artifacts: runtimeArtifactVerifier(root), Registry: registry, Sessions: &fakeSessionPersister{},
		Observer: observer, Retention: runtimeTestRetention{},
	})
	if err != nil {
		t.Fatalf("NewRuntimeDaemon: %v", err)
	}
	runtime, daemon := rootComposition.Runtime, rootComposition.Daemon
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	request := runtimeCreateRequest(prepared, binding)
	created := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleCreate, Binding: control.Binding{Owner: binding.Owner, SessionID: binding.SessionID}, Create: &request})
	if created.Err != nil || created.Record == nil || created.Record.State != EnvironmentReady {
		t.Fatalf("create through daemon = %+v", created)
	}
	if !backend.launch.Verified || !backend.launch.HostReadOnly || backend.launch.NetworkSocket != provider.socket || backend.launch.VsockPort != control.GuestControlPort {
		t.Fatalf("concrete launch omitted verified/read-only/network/vsock wiring: %+v", backend.launch)
	}
	provider.denials = 2
	if got := observer.Snapshot().EgressDenials; got != 2 {
		t.Fatalf("active runtime egress denials before destruction = %d, want 2", got)
	}
	for _, mount := range backend.launch.Mounts {
		if mount.OverrideUID != 0 || mount.OverrideGID != 0 {
			t.Fatalf("mount %q relies on unsupported ownership override %d:%d", mount.Tag, mount.OverrideUID, mount.OverrideGID)
		}
	}
	worktreeInfo, err := os.Stat(prepared.WorktreePath)
	if err != nil || worktreeInfo.Mode().Perm() != 0o700 {
		t.Fatalf("private worktree mode = %v, %v", worktreeInfo.Mode().Perm(), err)
	}

	services, err := runtime.Services(created.Record.Ref)
	if err != nil {
		t.Fatalf("resolve runtime services: %v", err)
	}
	if _, err := services.Workspace.CreateFile(ctx, "runtime.txt", []byte("guest workspace\n")); err != nil {
		t.Fatalf("guest Workspace create: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(prepared.WorktreePath, "runtime.txt"))
	if err != nil || string(contents) != "guest workspace\n" {
		t.Fatalf("host-visible guest write = %q, %v", contents, err)
	}
	result, err := services.Runner.Run(ctx, "printf concrete-runtime")
	if err != nil || result.Stdout != "concrete-runtime" || result.ExitCode != 0 {
		t.Fatalf("guest exec = %#v, %v", result, err)
	}

	if response := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDetach, Binding: created.Binding}); response.Err != nil {
		t.Fatalf("detach through daemon: %v", response.Err)
	}
	if response := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: created.Binding}); response.Err != nil {
		t.Fatalf("reattach through daemon: %v", response.Err)
	}
	if _, err := runtime.Services(created.Record.Ref); err != nil {
		t.Fatalf("reattach did not restore exact guest services: %v", err)
	}
	cancel()
	if err := provider.startCtx.Err(); err != nil {
		t.Fatalf("generation network inherited create cancellation: %v", err)
	}
	if response := daemon.Handle(context.Background(), server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDelete, Binding: created.Binding}); response.Err != nil {
		t.Fatalf("delete through daemon: %v", response.Err)
	}
	if backend.hostFallback || backend.stopCalls != 1 || backend.removeCalls != 1 || !provider.stopped {
		t.Fatalf("lifecycle/fallback drift: backend=%+v provider=%+v", backend, provider)
	}
}

type staticRuntimeIdentity struct{ identity EnvironmentIdentity }

func (s staticRuntimeIdentity) Allocate(string) (EnvironmentIdentity, error) { return s.identity, nil }

type staticRuntimeWorktree struct{ prepared *worktree.Prepared }

func (s staticRuntimeWorktree) Prepare(context.Context, worktree.Request) (*worktree.Prepared, error) {
	return s.prepared, nil
}
func (staticRuntimeWorktree) Cleanup(context.Context, *worktree.Prepared) error { return nil }

type prebootIPv6Only struct{}

func (prebootIPv6Only) DisableIPv6(context.Context) error { return nil }

type runtimeTestRetention struct{}

func (runtimeTestRetention) Dirty(context.Context, EnvironmentRecord) (bool, error) {
	return false, nil
}
func (runtimeTestRetention) Cleanup(context.Context, EnvironmentRecord) error { return nil }

type runtimeTestNetwork struct {
	socket   string
	stopped  bool
	denials  uint64
	startCtx context.Context
}

func (n *runtimeTestNetwork) Start(ctx context.Context, _ gomicrovmnet.Config) error {
	n.startCtx = ctx
	return nil
}
func (n *runtimeTestNetwork) SocketPath() string    { return n.socket }
func (n *runtimeTestNetwork) Stop()                 { n.stopped = true }
func (n *runtimeTestNetwork) EgressDenials() uint64 { return n.denials }

type runtimeTestBackend struct {
	t            *testing.T
	launch       GoMicroVMLaunch
	host         net.Conn
	guest        net.Conn
	serveErr     chan error
	stopCalls    int
	removeCalls  int
	hostFallback bool
}

func newRuntimeTestBackend(t *testing.T) *runtimeTestBackend {
	return &runtimeTestBackend{t: t, serveErr: make(chan error, 1)}
}

func (b *runtimeTestBackend) Start(ctx context.Context, launch GoMicroVMLaunch) (GoMicroVMInstance, error) {
	b.launch = launch
	b.hostFallback = !launch.Verified || !launch.HostReadOnly || launch.Network == nil || launch.VsockPort == 0
	if err := b.startGuest(ctx); err != nil {
		return nil, err
	}
	return &runtimeTestInstance{backend: b, launch: launch}, nil
}

func (b *runtimeTestBackend) startGuest(ctx context.Context) error {
	b.host, b.guest = net.Pipe()
	server, err := guestagent.NewServer(guestagent.ServerConfig{
		Binding: b.launch.Preboot.Config.Binding, CapabilityKey: b.launch.CapabilityKey,
		WorkspaceRoot:    b.launch.Prepared.WorktreePath,
		WorkloadIdentity: guestexec.WorkloadIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())},
	})
	if err != nil {
		return err
	}
	go func() { b.serveErr <- server.Serve(ctx, b.guest) }()
	return nil
}

func (b *runtimeTestBackend) Open(context.Context, EnvironmentRecord) (GoMicroVMInstance, error) {
	return &runtimeTestInstance{backend: b, launch: b.launch}, nil
}
func (b *runtimeTestBackend) DialGuest(ctx context.Context, _ string) (io.ReadWriteCloser, error) {
	if b.host == nil {
		if err := b.startGuest(ctx); err != nil {
			return nil, err
		}
	}
	host := b.host
	b.host = nil
	return host, nil
}

type runtimeTestInstance struct {
	backend *runtimeTestBackend
	launch  GoMicroVMLaunch
}

func (*runtimeTestInstance) WaitReady(context.Context) error { return nil }
func (i *runtimeTestInstance) Status(context.Context) (RuntimeStatus, error) {
	return RuntimeStatus{Live: true, Generation: i.launch.Generation, VMID: i.launch.VMID, PID: 4242, ProcessIdentity: "boot:4242", Endpoint: i.launch.Endpoint}, nil
}
func (i *runtimeTestInstance) Stop(context.Context) error {
	i.backend.stopCalls++
	if i.backend.guest != nil {
		_ = i.backend.guest.Close()
	}
	return nil
}
func (i *runtimeTestInstance) Remove(context.Context) error { i.backend.removeCalls++; return nil }

func preparedRuntimeWorktree(t *testing.T, root string) *worktree.Prepared {
	t.Helper()
	prepared := &worktree.Prepared{
		SourceRoot: filepath.Join(root, "source"), WorktreePath: filepath.Join(root, "worktree"),
		MetadataPath: filepath.Join(root, "metadata"), CommonObjectStore: filepath.Join(root, "objects"), Branch: "mecatl/runtime",
	}
	for _, dir := range []string{prepared.SourceRoot, prepared.WorktreePath, prepared.MetadataPath, prepared.CommonObjectStore} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return prepared
}

func runtimeArtifactVerifier(root string) ArtifactVerifier {
	artifact := func(kind ArtifactKind) VerifiedArtifact {
		path := filepath.Join(root, string(kind))
		_ = os.WriteFile(path, []byte(kind), 0o600)
		return VerifiedArtifact{Kind: kind, Digest: "sha256:" + string(kind), Path: path}
	}
	return &fakeArtifactVerifier{verified: VerifiedArtifacts{
		Runtime: artifact(ArtifactRuntime), Firmware: artifact(ArtifactFirmware),
		ExecutionImage: artifact(ArtifactExecutionImage), GuestAgent: artifact(ArtifactGuestAgent),
	}, policyRevision: "policy-runtime"}
}

func TestGoMicroVMRuntime_PrebootDoesNotMutateVerifiedImageOrFollowSymlinks(t *testing.T) {
	t.Parallel()
	preboot := GuestPrebootArtifact{RootfsPath: GuestPrebootConfigPath, Config: GuestPrebootConfig{Binding: control.Binding{Owner: "owner", SessionID: "session", EnvironmentID: "env", Ref: "env@1", Generation: 1}}}
	key := []byte("0123456789abcdef0123456789abcdef")

	t.Run("immutable source", func(t *testing.T) {
		source, destination := t.TempDir(), filepath.Join(t.TempDir(), "rootfs")
		if err := os.MkdirAll(filepath.Join(source, "etc"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := prepareExecutionRootFS(source, destination, preboot, key); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(source, "etc", "mecatl", "guest-agent.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("verified source image was mutated: %v", err)
		}
		configPath := filepath.Join(destination, "etc", "mecatl", "guest-agent.json")
		info, err := os.Stat(configPath)
		if err != nil {
			t.Fatalf("private rootfs has no preboot config: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("preboot capability material mode = %#o, want 0600", info.Mode().Perm())
		}
		parentInfo, err := os.Stat(filepath.Dir(configPath))
		if err != nil {
			t.Fatalf("stat preboot capability directory: %v", err)
		}
		if parentInfo.Mode().Perm() != 0o700 {
			t.Fatalf("preboot capability directory mode = %#o, want 0700", parentInfo.Mode().Perm())
		}
	})

	t.Run("symlink parent", func(t *testing.T) {
		source, outside := t.TempDir(), t.TempDir()
		if err := os.MkdirAll(filepath.Join(source, "etc"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(source, "etc", "mecatl")); err != nil {
			t.Fatal(err)
		}
		if err := prepareExecutionRootFS(source, filepath.Join(t.TempDir(), "rootfs"), preboot, key); err == nil {
			t.Fatal("preboot installation followed an image-controlled parent symlink")
		}
		if _, err := os.Stat(filepath.Join(outside, "guest-agent.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("preboot escaped private rootfs: %v", err)
		}
	})
}

func runtimeCreateRequest(prepared *worktree.Prepared, binding control.Binding) CreateRequest {
	requests := make(map[ArtifactKind]ArtifactRequest)
	for _, kind := range []ArtifactKind{ArtifactRuntime, ArtifactFirmware, ArtifactExecutionImage, ArtifactGuestAgent} {
		requests[kind] = ArtifactRequest{Kind: kind, Reference: string(kind) + "@sha256:" + string(kind), Digest: "sha256:" + string(kind)}
	}
	return CreateRequest{
		Owner: binding.Owner, SessionID: binding.SessionID, Profile: "secure",
		Worktree:         worktree.Request{Source: prepared.SourceRoot, WorktreePath: prepared.WorktreePath, MetadataPath: prepared.MetadataPath, Branch: prepared.Branch},
		ArtifactRequests: requests,
	}
}
