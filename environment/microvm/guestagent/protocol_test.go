package guestagent_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
)

var multiplexBinding = control.Binding{
	Owner:         "caller:alice",
	SessionID:     "session-1",
	EnvironmentID: "environment-1",
	Ref:           "microvm:environment-1",
	Generation:    7,
}

func TestGuestProtocol_MultiplexesWorkspaceAndExecAfterOneHandshake(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("workspace-data"), 0o600); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	server, err := guestagent.NewServer(guestagent.ServerConfig{
		Binding: multiplexBinding, CapabilityKey: key, WorkspaceRoot: root,
		WorkloadIdentity: guestexec.WorkloadIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	issuer, err := control.NewCapabilityIssuer(key)
	if err != nil {
		t.Fatalf("NewCapabilityIssuer: %v", err)
	}
	capability, err := issuer.Issue(multiplexBinding)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	host, guest := net.Pipe()
	var handshakes atomic.Int32
	serveDone := make(chan error, 1)
	go func() {
		handshakes.Add(1)
		serveDone <- server.Serve(context.Background(), guest)
	}()
	services, err := guestagent.Connect(context.Background(), host, multiplexBinding, capability)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	data, err := services.Workspace.Read(context.Background(), "input.txt")
	if err != nil || string(data) != "workspace-data" {
		t.Fatalf("workspace read = %q, %v", data, err)
	}
	result, err := services.Runner.Run(context.Background(), "printf exec-data")
	if err != nil || result.Stdout != "exec-data" || result.Stderr != "" || result.ExitCode != 0 {
		t.Fatalf("exec result = %#v, %v", result, err)
	}
	if handshakes.Load() != 1 {
		t.Fatalf("guest connections/handshakes = %d, want 1", handshakes.Load())
	}
	if err := services.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := <-serveDone; err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Serve: %v", err)
	}
}

func TestGuestProtocol_TransferableCapabilityRejectsReplayAndWrongGeneration(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	server, err := guestagent.NewServer(guestagent.ServerConfig{
		Binding: multiplexBinding, CapabilityKey: key, WorkspaceRoot: t.TempDir(),
		WorkloadIdentity: guestexec.WorkloadIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	issuer, err := control.NewCapabilityIssuer(append([]byte(nil), key...))
	if err != nil {
		t.Fatalf("NewCapabilityIssuer: %v", err)
	}
	capability, err := issuer.Issue(multiplexBinding)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	connect := func(binding control.Binding, token string) error {
		host, guest := net.Pipe()
		done := make(chan error, 1)
		go func() { done <- server.Serve(context.Background(), guest) }()
		services, connectErr := guestagent.Connect(context.Background(), host, binding, token)
		if connectErr == nil {
			_ = services.Close()
		}
		_ = host.Close()
		_ = guest.Close()
		<-done
		return connectErr
	}
	if err := connect(multiplexBinding, capability); err != nil {
		t.Fatalf("first capability use: %v", err)
	}
	if err := connect(multiplexBinding, capability); !errors.Is(err, control.ErrUnauthenticatedCapability) {
		t.Fatalf("replayed capability: got %v, want ErrUnauthenticatedCapability", err)
	}

	fresh, err := issuer.Issue(multiplexBinding)
	if err != nil {
		t.Fatalf("Issue fresh: %v", err)
	}
	wrongGeneration := multiplexBinding
	wrongGeneration.Generation++
	if err := connect(wrongGeneration, fresh); !errors.Is(err, control.ErrUnauthenticatedCapability) {
		t.Fatalf("wrong generation: got %v, want ErrUnauthenticatedCapability", err)
	}
}
