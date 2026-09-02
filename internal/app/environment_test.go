package app

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

func testEnvironment(ws tool.Workspace, runner tool.CommandRunner) tool.Environment {
	return tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: ws.Root(), Revision: "test-v1"}, ws, runner)
}

func memEnvironment(root string) tool.Environment {
	return testEnvironment(memfs.NewWorkspace(root), nil)
}

// osfsEnvironment builds an osfs Environment rooted at dir with an OPTIONAL
// bound command runner, failing the test on error. It is the Environment-seam
// analogue of osfsWSForTest for the app tests that drive an engine or
// delegation tool against a real on-disk workspace (issue #462).
func osfsEnvironment(t *testing.T, dir string, runner tool.CommandRunner) tool.Environment {
	t.Helper()
	ws, err := osfs.NewWorkspace(dir)
	if err != nil {
		t.Fatalf("osfs workspace %q: %v", dir, err)
	}
	return tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: dir, Revision: "test-v1"}, ws, runner)
}
