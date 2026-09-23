package boatenv

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// fakeWorkspace binds a fresh sandbox on the offline fake API and returns its
// Workspace, so the shared tables exercise the real Go client, the real guest
// helper, and the same wire protocol the live API speaks.
func fakeWorkspace(t *testing.T) tool.Workspace {
	t.Helper()
	requireLocalHelper(t)
	fake := newFakeBoatAPI(t)
	provider, err := New(Config{
		APIKey: "test-key", BaseURL: fake.server.URL, HTTPClient: fake.server.Client(),
		Scope: "conformance", TTLSeconds: 60, ReadyTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.client.poll = time.Millisecond
	binding, err := provider.Bind(context.Background(), server.PlacementBindRequest{
		Selector: server.DefaultPlacement(), Scope: "conformance", Operation: server.PlacementOperationCreate,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Close() })
	return binding.Environment.Workspace()
}

// TestConformance runs the shared Workspace conformance table (ADR 0315)
// against the Boat Workspace.
func TestConformance(t *testing.T) {
	fsconformance.Run(t, fakeWorkspace)
}

// TestNamespaceConformance runs the shared WorkspaceNamespace table
// (ReadDir/Remove/Rename/CopyFile) against the Boat Workspace.
func TestNamespaceConformance(t *testing.T) {
	fsconformance.RunNamespace(t, fakeWorkspace)
}
