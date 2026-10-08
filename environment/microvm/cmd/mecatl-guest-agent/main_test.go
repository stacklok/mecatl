//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
)

func TestRepositoryGuestServesConcurrentLogicalConnections(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	var active atomic.Int32
	dial := func(context.Context) (io.ReadWriteCloser, error) {
		guest, host := net.Pipe()
		_ = host.Close()
		return guest, nil
	}
	serve := func(ctx context.Context, _ io.ReadWriteCloser) error {
		if active.Add(1) == 2 {
			close(started)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- serveRepositoryConnections(ctx, dial, serve) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("repository guest serialized logical connections")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serveRepositoryConnections() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("repository connection accept loop did not stop")
	}
}

func TestRepositoryGuestWaitsForDelayedHostChallenge(t *testing.T) {
	const (
		owner         = "operator"
		repositoryKey = "repository"
		vmID          = "vm-1"
		generation    = 7
		endpoint      = "/guest.sock"
	)
	key := []byte("0123456789abcdef0123456789abcdef")
	identity := guestexec.WorkloadIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	runtimeContract := guestexec.DefaultRuntimeContract()
	runtimeContract.Identity = identity
	server, err := guestagent.NewRepositoryServer(guestagent.RepositoryServerConfig{
		Owner: owner, RepositoryKey: repositoryKey, VMID: vmID, Endpoint: endpoint, Generation: generation,
		AuthorityKey: key, WorkloadIdentity: identity, RuntimeContract: runtimeContract,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	issuer, err := control.NewCapabilityIssuer(key)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	connections := make(chan io.ReadWriteCloser, maxRepositoryConnections)
	dial := func(ctx context.Context) (io.ReadWriteCloser, error) {
		guest, host := net.Pipe()
		select {
		case connections <- host:
			return guest, nil
		case <-ctx.Done():
			_ = guest.Close()
			_ = host.Close()
			return nil, ctx.Err()
		}
	}
	done := make(chan error, 1)
	go func() { done <- serveRepositoryConnections(ctx, dial, server.ServeAuthenticated) }()
	defer func() {
		cancel()
		select {
		case loopErr := <-done:
			if !errors.Is(loopErr, context.Canceled) {
				t.Errorf("serveRepositoryConnections() = %v", loopErr)
			}
		case <-time.After(time.Second):
			t.Error("repository connection loop did not stop")
		}
	}()

	// The live host can take longer than the host-side unauthenticated-peer budget
	// to accept a reverse-vsock connection queued by the guest.
	time.Sleep(350 * time.Millisecond)

	nextConnection := func() io.ReadWriteCloser {
		t.Helper()
		select {
		case stream := <-connections:
			return stream
		case <-time.After(time.Second):
			t.Fatal("guest did not originate a repository connection")
			return nil
		}
	}
	controlExchange := func(request guestagent.RepositoryControlRequest) {
		t.Helper()
		stream := nextConnection()
		defer stream.Close()
		if err := guestagent.AuthenticateHostRepositoryChannel(ctx, stream, key, owner, repositoryKey, vmID, generation, guestagent.RepositoryChannelControl); err != nil {
			t.Fatalf("authenticate control connection: %v", err)
		}
		codec := control.NewCodec(control.DefaultMaxMessageBytes)
		if err := codec.Write(stream, request); err != nil {
			t.Fatal(err)
		}
		var response guestagent.RepositoryControlResponse
		if err := codec.Read(stream, &response); err != nil {
			t.Fatal(err)
		}
		if response.ErrorCode != "" {
			t.Fatalf("control response = %q", response.ErrorCode)
		}
	}
	register := func(binding control.Binding) {
		t.Helper()
		capability, issueErr := issuer.Issue(binding)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		controlExchange(guestagent.RepositoryControlRequest{Operation: guestagent.RepositoryRegister, Binding: binding, Capability: capability})
	}
	useData := func(binding control.Binding, want string) {
		t.Helper()
		capability, issueErr := issuer.Issue(binding)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		stream := nextConnection()
		if authErr := guestagent.AuthenticateHostRepositoryChannel(ctx, stream, key, owner, repositoryKey, vmID, generation, guestagent.RepositoryChannelData); authErr != nil {
			_ = stream.Close()
			t.Fatalf("authenticate data connection: %v", authErr)
		}
		services, connectErr := guestagent.Connect(ctx, stream, binding, capability)
		if connectErr != nil {
			_ = stream.Close()
			t.Fatalf("connect data services: %v", connectErr)
		}
		defer services.Close()
		got, readErr := services.Workspace.Read(ctx, "value.txt")
		if readErr != nil || string(got) != want {
			t.Fatalf("data read = %q, %v; want %q", got, readErr, want)
		}
	}
	newBinding := func(name string) control.Binding {
		t.Helper()
		root := filepath.Join(t.TempDir(), "worktree")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "value.txt"), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		return control.Binding{Owner: owner, SessionID: "session-" + name, EnvironmentID: "logical-" + name, Ref: "logical-" + name, Generation: generation, AssignedRoot: root}
	}

	first := newBinding("first")
	register(first)
	useData(first, "first")
	controlExchange(guestagent.RepositoryControlRequest{Operation: guestagent.RepositoryHealth, Health: &guestagent.RepositoryHealthChallenge{
		Owner: owner, RepositoryKey: repositoryKey, VMID: vmID, Generation: generation,
		Status: guestagent.RepositoryHealthStatus{Live: true, Generation: generation, VMID: vmID, PID: 42, ProcessIdentity: "boot", Endpoint: endpoint},
	}})
	second := newBinding("second")
	register(second)
	useData(second, "second")
}

func TestRootlessSafeChownAcceptsUnmappedGuestRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guest-agent.json")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := rootlessSafeChownWith(path, 0, 0, func(string, int, int) error {
		return syscall.EINVAL
	}, os.Lstat)
	if err != nil {
		t.Fatalf("unmapped guest-root chown: %v", err)
	}
}

func TestConsumePrivilegedConfigRemovesWorkloadOwnedMaterial(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "mecatl")
	path := filepath.Join(parent, "guest-agent.json")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := consumePrivilegedConfig(path); err != nil {
		t.Fatalf("consume privileged config: %v", err)
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("capability material remains reachable: %v", err)
	}
}

func TestInvariant_guest_capability_material_is_agent_only(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mecatl", "guest-agent.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	type ownership struct {
		path     string
		uid, gid int
	}
	var owners []ownership
	modes := make(map[string]os.FileMode)
	err := hardenPrivilegedConfig(path,
		func(path string, uid, gid int) error {
			owners = append(owners, ownership{path: path, uid: uid, gid: gid})
			return nil
		},
		func(path string, mode os.FileMode) error {
			modes[path] = mode
			return nil
		},
	)
	if err != nil {
		t.Fatalf("hardenPrivilegedConfig: %v", err)
	}
	if len(owners) != 2 {
		t.Fatalf("ownership operations = %v, want config and parent", owners)
	}
	for _, owner := range owners {
		if owner.uid != 0 || owner.gid != 0 {
			t.Fatalf("privileged config ownership = %v, want guest root", owners)
		}
	}
	if modes[path] != 0o600 || modes[filepath.Dir(path)] != 0o700 {
		t.Fatalf("privileged config modes = %v, want file 0600 and parent 0700", modes)
	}
}
