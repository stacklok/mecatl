package guestagent

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
)

func TestAuthenticateHostRepositoryChannelTimesOutUnauthenticatedPeer(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	host, guest := net.Pipe()
	defer guest.Close()
	go func() { _, _ = io.Copy(io.Discard, guest) }()

	started := time.Now()
	err := AuthenticateHostRepositoryChannel(t.Context(), host, key, "operator", "repository", "vm-1", 7, RepositoryChannelData)
	if !errors.Is(err, ErrUnauthenticatedRepositoryChannel) {
		t.Fatalf("AuthenticateHostRepositoryChannel() = %v, want ErrUnauthenticatedRepositoryChannel", err)
	}
	if elapsed := time.Since(started); elapsed < repositoryChannelAuthTimeout/2 || elapsed > 4*repositoryChannelAuthTimeout {
		t.Fatalf("unauthenticated host exchange took %v, want timeout near %v", elapsed, repositoryChannelAuthTimeout)
	}
}

func TestRepositoryControlCapacitySurvivesFullLogicalAttachmentCapacity(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	identity := guestexec.WorkloadIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	runtimeContract := guestexec.DefaultRuntimeContract()
	runtimeContract.Identity = identity
	server, err := NewRepositoryServer(RepositoryServerConfig{
		Owner: "operator", RepositoryKey: "repository", VMID: "vm-1", Endpoint: "/guest.sock", Generation: 7,
		AuthorityKey: key, WorkloadIdentity: identity, RuntimeContract: runtimeContract,
		ResolveRoot: func(root string) (string, error) { return root, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := control.NewCapabilityIssuer(key)
	if err != nil {
		t.Fatal(err)
	}

	services := make([]*Services, 0, maxRepositoryDataConnections)
	for i := range maxRepositoryDataConnections {
		root := filepath.Join(t.TempDir(), "worktree")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		binding := control.Binding{Owner: "operator", SessionID: "session", EnvironmentID: "logical", Ref: "logical-" + string(rune('a'+i)), Generation: 7, AssignedRoot: root}
		registration, issueErr := issuer.Issue(binding)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		if err := server.Register(t.Context(), registration, binding); err != nil {
			t.Fatalf("register attachment %d: %v", i, err)
		}
		capability, issueErr := issuer.Issue(binding)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		host, guest := net.Pipe()
		go func() {
			_ = server.ServeAuthenticated(t.Context(), guest)
			_ = guest.Close()
		}()
		if authErr := AuthenticateHostRepositoryChannel(t.Context(), host, key, "operator", "repository", "vm-1", 7, RepositoryChannelData); authErr != nil {
			t.Fatalf("authenticate attachment %d: %v", i, authErr)
		}
		connected, connectErr := Connect(t.Context(), host, binding, capability)
		if connectErr != nil {
			t.Fatalf("connect attachment %d: %v", i, connectErr)
		}
		services = append(services, connected)
	}
	defer func() {
		for _, connected := range services {
			_ = connected.Close()
		}
	}()

	exchange := func(name string, request RepositoryControlRequest) {
		t.Helper()
		host, guest := net.Pipe()
		done := make(chan error, 1)
		go func() {
			done <- server.ServeAuthenticated(t.Context(), guest)
			_ = guest.Close()
		}()
		if err := AuthenticateHostRepositoryChannel(t.Context(), host, key, "operator", "repository", "vm-1", 7, RepositoryChannelControl); err != nil {
			t.Fatalf("%s channel authentication: %v", name, err)
		}
		codec := control.NewCodec(control.DefaultMaxMessageBytes)
		if err := codec.Write(host, request); err != nil {
			t.Fatal(err)
		}
		var response RepositoryControlResponse
		if err := codec.Read(host, &response); err != nil {
			t.Fatalf("%s control starved at full logical capacity: %v", name, err)
		}
		if response.ErrorCode != "" {
			t.Fatalf("%s control rejected at full logical capacity: %s", name, response.ErrorCode)
		}
		_ = host.Close()
		select {
		case serveErr := <-done:
			if serveErr != nil && serveErr != io.EOF {
				t.Fatalf("%s control exchange: %v", name, serveErr)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s control did not complete at full logical capacity", name)
		}
	}
	exchange("health", RepositoryControlRequest{Operation: RepositoryHealth, Health: &RepositoryHealthChallenge{
		Owner: "operator", RepositoryKey: "repository", VMID: "vm-1", Generation: 7,
		Status: RepositoryHealthStatus{Live: true, Generation: 7, VMID: "vm-1", PID: 42, ProcessIdentity: "boot", Endpoint: "/guest.sock"},
	}})

	controlRoot := filepath.Join(t.TempDir(), "control-worktree")
	if err := os.Mkdir(controlRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	controlBinding := control.Binding{Owner: "operator", SessionID: "control-session", EnvironmentID: "control-logical", Ref: "logical-control", Generation: 7, AssignedRoot: controlRoot}
	registration, err := issuer.Issue(controlBinding)
	if err != nil {
		t.Fatal(err)
	}
	exchange("register", RepositoryControlRequest{Operation: RepositoryRegister, Binding: controlBinding, Capability: registration})
	unregistration, err := issuer.Issue(controlBinding)
	if err != nil {
		t.Fatal(err)
	}
	exchange("unregister", RepositoryControlRequest{Operation: RepositoryUnregister, Binding: controlBinding, Capability: unregistration})
}
