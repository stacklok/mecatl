package workspace_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/workspace"
)

func TestInvariant_environment_workspace_runner_affinity(t *testing.T) {
	ws, _ := newRemoteFixture(t)
	if ws.Root() != "/workspace" {
		t.Fatalf("Workspace root = %q, want bound runner cwd /workspace", ws.Root())
	}
	fsconformance.RunMutationAffinity(t, newRemoteFixture)
}

func TestMicroVMEnvironments_Scenario3_RemoteWorkspaceConformance(t *testing.T) {
	fsconformance.Run(t, func(t *testing.T) tool.Workspace {
		ws, _ := newRemoteFixture(t)
		return ws
	})
	t.Run("external mutation conflicts", func(t *testing.T) {
		fsconformance.RunExternalMutationConflict(t, newRemoteFixture)
	})
	t.Run("bounded read", func(t *testing.T) {
		ws, external := newRemoteFixture(t)
		if err := external.Write("oversized.txt", bytes.Repeat([]byte("x"), (512<<10)+1)); err != nil {
			t.Fatalf("seed oversized file: %v", err)
		}
		if _, err := ws.Read(context.Background(), "oversized.txt"); err == nil {
			t.Fatal("Read oversized result succeeded, want bounded-result error")
		}
	})
}

func newRemoteFixture(t *testing.T) (tool.Workspace, fsconformance.ExternalAccess) {
	t.Helper()
	root := t.TempDir()
	binding := control.Binding{
		Owner:         "owner-1",
		SessionID:     "session-1",
		EnvironmentID: "environment-1",
		Ref:           "microvm:environment-1:1",
		Generation:    1,
	}
	host, guestConn := net.Pipe()
	server, err := workspace.NewGuest(root, binding)
	if err != nil {
		t.Fatalf("NewGuest: %v", err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	verifier, err := control.NewCapabilityVerifier(key)
	if err != nil {
		t.Fatalf("NewCapabilityVerifier: %v", err)
	}
	issuer, err := control.NewCapabilityIssuer(key)
	if err != nil {
		t.Fatalf("NewCapabilityIssuer: %v", err)
	}
	capability, err := issuer.Issue(binding)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- control.ServeMultiplex(context.Background(), guestConn, binding, verifier, map[control.ServiceName]control.Handler{
			control.ServiceWorkspace: server.Handler(),
		}, control.DefaultMaxMessageBytes)
	}()
	client, err := control.OpenClient(context.Background(), host, binding, capability, []control.ServiceName{control.ServiceWorkspace}, control.DefaultMaxMessageBytes)
	if err != nil {
		_ = host.Close()
		_ = guestConn.Close()
		t.Fatalf("OpenClient: %v", err)
	}
	ws, err := workspace.New(client, binding)
	if err != nil {
		_ = client.Close()
		_ = guestConn.Close()
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = guestConn.Close()
		_ = server.Close()
		if err := <-serveErr; err != nil && err != io.EOF {
			t.Errorf("guest ServeMultiplex: %v", err)
		}
	})

	return ws, fsconformance.ExternalAccess{
		Read: func(path string) ([]byte, error) {
			return os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		},
		Write: func(path string, data []byte) error {
			full := filepath.Join(root, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			return os.WriteFile(full, data, 0o644)
		},
	}
}
