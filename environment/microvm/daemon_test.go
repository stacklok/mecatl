package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/control/controltest"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
)

func TestMicroVMDaemon_DaemonInfoIsAuthenticatedAndExact(t *testing.T) {
	t.Parallel()
	want := DaemonInfo{
		ProtocolVersion: LifecycleProtocolVersion,
		ReleaseIdentity: "sha256:release", BinaryIdentity: "sha256:binary", ConfigDigest: "sha256:config",
		PolicyRevision: "release-v1", Profiles: []string{"microvm-local"}, Socket: "/run/user/1000/microvmd.sock",
	}
	newDaemon := func(peerUID uint32) *Daemon {
		t.Helper()
		auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: peerUID}})
		if err != nil {
			t.Fatal(err)
		}
		record := readyRecord("info", 1)
		daemon, err := NewDaemon(DaemonConfig{Control: auth, Registry: newLifecycleRegistry(record), Runtime: &lifecycleRuntime{live: map[string]RuntimeStatus{}}, Worktrees: &lifecycleWorktrees{}, Info: want})
		if err != nil {
			t.Fatal(err)
		}
		return daemon
	}

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	response := newDaemon(1000).Handle(context.Background(), server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleInfo})
	if response.Err != nil {
		t.Fatal(response.Err)
	}
	var got DaemonInfo
	if err := json.Unmarshal(response.Payload, &got); err != nil || !got.Equal(want) {
		t.Fatalf("daemon info = %+v, %v; want %+v", got, err, want)
	}

	foreignServer, foreignClient := net.Pipe()
	defer foreignServer.Close()
	defer foreignClient.Close()
	foreign := newDaemon(1001).Handle(context.Background(), foreignServer, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleInfo})
	if !errors.Is(foreign.Err, control.ErrUnauthenticatedPeer) || len(foreign.Payload) != 0 {
		t.Fatalf("foreign owner received daemon identity: %+v", foreign)
	}
}

func TestMicroVMDaemon_InfoFailsNamedRestartHealthPhaseWhenRepositoryBackendWasLost(t *testing.T) {
	t.Parallel()
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	restartErr := errors.New("repository restart health phase: hosted network backend is not live")
	daemon, err := NewDaemon(DaemonConfig{
		Control: auth, Registry: newLifecycleRegistry(readyRecord("restart-health", 1)), Runtime: &lifecycleRuntime{live: map[string]RuntimeStatus{}}, Worktrees: &lifecycleWorktrees{},
		Info: DaemonInfo{ProtocolVersion: LifecycleProtocolVersion}, RepositoryStartupError: restartErr,
	})
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	response := daemon.Handle(t.Context(), server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleInfo})
	if !errors.Is(response.Err, restartErr) || len(response.Payload) != 0 {
		t.Fatalf("daemon info restart health = %+v", response)
	}
}

func TestMicroVMDaemon_DelegationOperationsAreGenerationBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	parent := readyRecord("parent-rpc", 3)
	child := readyRecord("child-rpc", 4)
	child.SessionID = "parallel-parent-rpc-1"
	child.ParentRef = parent.Ref
	child.ForkBase = "base-commit"
	registry := newLifecycleRegistry(parent, child)
	children := &daemonTestChildren{child: child}
	runtime := &lifecycleRuntime{live: map[string]RuntimeStatus{parent.EnvironmentID: exactRuntimeStatus(parent), child.EnvironmentID: exactRuntimeStatus(child)}}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{Control: auth, Registry: registry, Runtime: runtime, Worktrees: &lifecycleWorktrees{}, Children: children})
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	forkPayload, _ := json.Marshal(ChildForkPayload{Label: "branch-1"})
	forked := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleFork, Binding: bindingForRecord(parent), Payload: forkPayload})
	if forked.Err != nil || forked.Binding != bindingForRecord(child) || forked.Record == nil || children.forks != 1 {
		t.Fatalf("fork response=%+v forks=%d", forked, children.forks)
	}
	mergePayload, _ := json.Marshal(ChildMergePayload{Child: bindingForRecord(child)})
	merged := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleMerge, Binding: bindingForRecord(parent), Payload: mergePayload})
	if merged.Err != nil || children.merges != 1 {
		t.Fatalf("merge response=%+v merges=%d", merged, children.merges)
	}

	stale := bindingForRecord(child)
	stale.Generation++
	mergePayload, _ = json.Marshal(ChildMergePayload{Child: stale})
	if response := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleMerge, Binding: bindingForRecord(parent), Payload: mergePayload}); !errors.Is(response.Err, control.ErrBindingMismatch) || children.merges != 1 {
		t.Fatalf("stale child reached merge: response=%+v merges=%d", response, children.merges)
	}
	deleted := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleChildDelete, Binding: bindingForRecord(child)})
	if deleted.Err != nil {
		t.Fatalf("child delete: %+v", deleted)
	}
	stored, err := registry.Lookup(ctx, child.EnvironmentID)
	if err != nil || stored.State != EnvironmentDestroyed || !stored.WorktreeDeleted {
		t.Fatalf("child cleanup was not durable: record=%+v err=%v", stored, err)
	}
}

type daemonTestChildren struct {
	child         EnvironmentRecord
	forks, merges int
}

func (d *daemonTestChildren) Fork(_ context.Context, parent EnvironmentRecord, label string) (EnvironmentRecord, error) {
	if parent.Ref != d.child.ParentRef || label == "" {
		return EnvironmentRecord{}, ErrInvalidFork
	}
	d.forks++
	return d.child, nil
}

func (d *daemonTestChildren) Merge(_ context.Context, parent, child EnvironmentRecord) error {
	if child.ParentRef != parent.Ref {
		return ErrInvalidFork
	}
	d.merges++
	return nil
}

func TestMicroVMDaemon_LifecycleRPCIsOwnerAndGenerationBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record := readyRecord("env-rpc", 7)
	createdRecord := readyRecord("env-created", 9)
	createdRecord.SessionID = "session-created"
	registry := newLifecycleRegistry(record)
	creator := &daemonTestCreator{registry: registry, record: createdRecord}
	runtime := &lifecycleRuntime{live: map[string]RuntimeStatus{
		record.EnvironmentID: exactRuntimeStatus(record), createdRecord.EnvironmentID: exactRuntimeStatus(createdRecord),
	}}
	worktrees := &lifecycleWorktrees{}
	auth, err := control.NewService(control.ServiceConfig{
		AccountUID:        1000,
		PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{
		Control: auth, Creator: creator, Registry: registry, Runtime: runtime, Worktrees: worktrees,
	})
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	createBinding := control.Binding{Owner: createdRecord.Owner, SessionID: createdRecord.SessionID}
	createRequest := CreateRequest{Owner: createdRecord.Owner, SessionID: createdRecord.SessionID}
	created := daemon.Handle(ctx, server, LifecycleRequest{
		Version: LifecycleProtocolVersion, Operation: LifecycleCreate, Binding: createBinding, Create: &createRequest,
	})
	if created.Err != nil || created.Binding != bindingForRecord(createdRecord) || creator.calls != 1 {
		t.Fatalf("create response is not durably generation-bound: response=%+v calls=%d", created, creator.calls)
	}
	staleCreate := createBinding
	staleCreate.EnvironmentID, staleCreate.Ref, staleCreate.Generation = "guessed", "guessed@4", 4
	if response := daemon.Handle(ctx, server, LifecycleRequest{
		Version: LifecycleProtocolVersion, Operation: LifecycleCreate, Binding: staleCreate, Create: &createRequest,
	}); !errors.Is(response.Err, control.ErrBindingMismatch) || creator.calls != 1 {
		t.Fatalf("preselected create generation reached creator: response=%+v calls=%d", response, creator.calls)
	}

	binding := bindingForRecord(record)
	mutations := map[string]func(*control.Binding){
		"owner":      func(claim *control.Binding) { claim.Owner = "caller:mallory" },
		"generation": func(claim *control.Binding) { claim.Generation++ },
	}
	for name, mutate := range mutations {
		for _, operation := range []LifecycleOperation{LifecycleResolve, LifecycleInspect, LifecycleDetach, LifecycleDelete} {
			claim := binding
			mutate(&claim)
			response := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: operation, Binding: claim})
			if !errors.Is(response.Err, control.ErrBindingMismatch) {
				t.Fatalf("%s with stale %s: got %v, want binding mismatch", operation, name, response.Err)
			}
		}
	}
	if runtime.detachCalls != 0 || runtime.destroyCalls != 0 {
		t.Fatalf("mismatched requests reached runtime: detaches=%d destroys=%d", runtime.detachCalls, runtime.destroyCalls)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- daemon.ServeConn(ctx, server)
	}()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(client, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleInspect, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	var wireResponse LifecycleResponse
	if err := codec.Read(client, &wireResponse); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil || wireResponse.Binding != binding || wireResponse.Record == nil {
		t.Fatalf("authenticated socket inspect response=%+v serveErr=%v", wireResponse, err)
	}

	for _, operation := range []LifecycleOperation{LifecycleResolve, LifecycleInspect, LifecycleDetach} {
		response := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: operation, Binding: binding})
		if response.Err != nil || response.Binding != binding || response.Record == nil || response.Record.Ref != record.Ref {
			t.Fatalf("%s exact generation response = %+v", operation, response)
		}
	}
	response := daemon.Handle(ctx, server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDelete, Binding: binding})
	if response.Err != nil || runtime.detachCalls != 1 || runtime.destroyCalls != 1 {
		t.Fatalf("delete exact generation response=%+v detaches=%d destroys=%d", response, runtime.detachCalls, runtime.destroyCalls)
	}
	got, err := registry.Lookup(ctx, record.EnvironmentID)
	if err != nil || got.State != EnvironmentDestroyed || !got.Tombstone {
		t.Fatalf("delete did not commit durable tombstone: record=%+v err=%v", got, err)
	}
}

func TestMicroVMDaemon_PrebootConfigDisablesIPv6BeforeWorkload(t *testing.T) {
	t.Parallel()
	fx := newLifecycleFixture("success")
	lifecycle := NewLifecycle(LifecycleDeps{
		Identities: fx.identities, Worktrees: fx.worktrees, Artifacts: fx.artifacts,
		VMs: fx.vms, Protocol: fx.protocol, Registry: fx.registry, Sessions: fx.sessions,
	})
	if _, err := lifecycle.Create(context.Background(), fx.request); err != nil {
		t.Fatal(err)
	}
	preboot := fx.vms.created.Preboot
	if preboot.RootfsPath != GuestPrebootConfigPath || !preboot.Config.DisableIPv6 {
		t.Fatalf("VM started without preboot IPv6 disablement: %+v", preboot)
	}
	wantBinding := fx.protocol.binding
	if preboot.Config.AgentEndpoint != fx.vms.created.Endpoint || preboot.Config.Binding != wantBinding {
		t.Fatalf("preboot guest-agent binding drifted from VM generation: %+v request=%+v", preboot.Config, fx.vms.created)
	}
	if !sameCapabilities(preboot.Config.Capabilities, control.RequiredCapabilities()) || preboot.Config.MaxMessageBytes != control.DefaultMaxMessageBytes {
		t.Fatalf("preboot capabilities are incomplete: %+v", preboot.Config)
	}
}

func TestDaemonExecDisconnectCancelsGuestGroupPreservesOutputAndReleasesSlot(t *testing.T) {
	record := readyRecord("exec-disconnect", 7)
	binding := bindingForRecord(record)
	root := t.TempDir()
	if os.Geteuid() == 0 {
		if err := os.Chmod(root, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	identity := guestexec.WorkloadIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	if identity.UID == 0 || identity.GID == 0 {
		identity = guestexec.DefaultWorkloadIdentity()
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	guest, err := guestagent.NewServer(guestagent.ServerConfig{
		Binding: binding, CapabilityKey: key, WorkspaceRoot: root,
		ExecLimits: guestexec.Limits{MaxConcurrent: 1, CancelGrace: 200 * time.Millisecond}, WorkloadIdentity: identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := control.NewCapabilityIssuer(key)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := issuer.Issue(binding)
	if err != nil {
		t.Fatal(err)
	}
	host, guestConn := net.Pipe()
	guestDone := make(chan error, 1)
	go func() { guestDone <- guest.Serve(context.Background(), guestConn) }()
	services, err := guestagent.Connect(context.Background(), host, binding, capability)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = services.Close()
		_ = guestConn.Close()
		if serveErr := <-guestDone; serveErr != nil && !errors.Is(serveErr, io.EOF) {
			t.Errorf("guest server: %v", serveErr)
		}
	}()

	baseRuntime := &lifecycleRuntime{live: map[string]RuntimeStatus{record.EnvironmentID: exactRuntimeStatus(record)}}
	runtime := &daemonServicesRuntime{lifecycleRuntime: baseRuntime, services: services}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{Control: auth, Registry: newLifecycleRegistry(record), Runtime: runtime, Worktrees: &lifecycleWorktrees{}})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"command": "printf 'partial\\n'; (trap '' TERM; while :; do :; done) & child=$!; printf '%s\\n' \"$child\"; wait"})
	request := LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleExec, Binding: binding, Payload: payload}

	serverConn, clientConn := net.Pipe()
	serveDone := make(chan error, 1)
	go func() { serveDone <- daemon.ServeConn(context.Background(), serverConn) }()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(clientConn, request); err != nil {
		t.Fatal(err)
	}
	var prior strings.Builder
	for len(strings.Fields(prior.String())) < 2 {
		var response LifecycleResponse
		if err := codec.Read(clientConn, &response); err != nil {
			t.Fatal(err)
		}
		if response.Stream != nil {
			prior.Write(response.Stream.Data)
		}
	}
	fields := strings.Fields(prior.String())
	childPID, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("child pid in %q: %v", prior.String(), err)
	}
	_ = clientConn.Close()
	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("peer disconnect did not cancel guest execution")
	}
	deadline := time.Now().Add(time.Second)
	for {
		err = syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("guest child %d survived disconnect: %v", childPID, err)
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(prior.String(), "partial\n") {
		t.Fatalf("prior output was lost: %q", prior.String())
	}

	payload, _ = json.Marshal(map[string]string{"command": "printf 'after\\n'"})
	request.Payload = payload
	serverConn, clientConn = net.Pipe()
	serveDone = make(chan error, 1)
	go func() { serveDone <- daemon.ServeConn(context.Background(), serverConn) }()
	if err := codec.Write(clientConn, request); err != nil {
		t.Fatal(err)
	}
	var after strings.Builder
	for {
		var response LifecycleResponse
		if err := codec.Read(clientConn, &response); err != nil {
			t.Fatal(err)
		}
		if response.Stream != nil {
			after.Write(response.Stream.Data)
			continue
		}
		if response.ErrorCode != "" {
			t.Fatalf("exec slot leaked after disconnect: %s", response.ErrorCode)
		}
		break
	}
	_ = clientConn.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("second exec: %v", err)
	}
	if after.String() != "after\n" {
		t.Fatalf("second exec output = %q", after.String())
	}
}

type daemonServicesRuntime struct {
	*lifecycleRuntime
	services *guestagent.Services
}

func (r *daemonServicesRuntime) Services(EnvironmentRef) (*guestagent.Services, error) {
	return r.services, nil
}

type daemonTestCreator struct {
	registry *lifecycleRegistry
	record   EnvironmentRecord
	calls    int
}

func (c *daemonTestCreator) Create(ctx context.Context, _ CreateRequest) (CreatedEnvironment, error) {
	c.calls++
	if err := c.registry.Save(ctx, c.record); err != nil {
		return CreatedEnvironment{}, err
	}
	return CreatedEnvironment{Ref: c.record.Ref, Generation: c.record.Generation, HostWorktree: c.record.WorktreePath, GuestRoot: c.record.GuestRoot}, nil
}

func sameCapabilities(left, right control.Capabilities) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
