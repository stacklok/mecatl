package remoteenv_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/remoteenv"
)

// TestConformance runs the shared Workspace conformance table against the
// remote-fake Workspace (ADR 0314: the capability is implemented for local OS,
// memory, Redis, and remote test workspaces).
func TestConformance(t *testing.T) {
	fsconformance.Run(t, func(t *testing.T) tool.Workspace {
		b := remoteenv.NewBackend()
		env, err := b.NewEnvironment("conformance")
		if err != nil {
			t.Fatalf("NewEnvironment: %v", err)
		}
		return env.Workspace()
	})
}

// TestNamespaceConformance runs the shared WorkspaceNamespace conformance
// table (ReadDir/Remove/Rename/CopyFile) against the remote-fake Workspace.
func TestNamespaceConformance(t *testing.T) {
	fsconformance.RunNamespace(t, func(t *testing.T) tool.Workspace {
		b := remoteenv.NewBackend()
		env, err := b.NewEnvironment("ns-conformance")
		if err != nil {
			t.Fatalf("NewEnvironment: %v", err)
		}
		return env.Workspace()
	})
}
