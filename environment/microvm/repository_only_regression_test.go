package microvm

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
)

func TestRuntimeDaemonRejectsMissingRepositoryComposition(t *testing.T) {
	if daemon, err := NewRuntimeDaemon(RuntimeDaemonConfig{RepositoryProvisioner: func(context.Context, ProvisionRequest) (RepositoryPlacement, error) {
		return RepositoryPlacement{}, nil
	}}); err == nil || daemon != nil {
		t.Fatalf("constructor accepted missing repository: daemon=%v err=%v", daemon, err)
	}
}

func TestRepositoryDaemonRejectsNonLogicalExecWithoutPanic(t *testing.T) {
	response := (&Daemon{}).handleExecStream(t.Context(), LifecycleRequest{
		Version: LifecycleProtocolVersion, Operation: LifecycleExec,
		Binding: control.Binding{Owner: "owner", SessionID: "session", EnvironmentID: "legacy", Ref: "legacy@1", Generation: 1},
	}, func(LifecycleExecStream) error { return nil })
	if !errors.Is(response.Err, ErrEnvironmentUnavailable) || response.ErrorCode != "unavailable" {
		t.Fatalf("nonlogical exec response = %+v", response)
	}
}

func TestProxyWorkspaceRejectsMissingReplaceVersionWithoutMutation(t *testing.T) {
	filesystem := memfs.NewWorkspace("/workspace")
	if _, err := filesystem.CreateFile(t.Context(), "tracked.txt", []byte("before")); err != nil {
		t.Fatal(err)
	}
	workspace := &replaceCountingWorkspace{Workspace: filesystem}
	response, err := proxyWorkspace(t.Context(), workspace, []byte(`{"operation":"replace","path":"tracked.txt","data":"YWZ0ZXI="}`))
	if err != nil {
		t.Fatalf("proxyWorkspace returned protocol error: %v", err)
	}
	if response.ErrorCode != "version_mismatch" || workspace.replaceCalls != 0 {
		t.Fatalf("invalid replace response = %+v, ReplaceFile calls = %d", response, workspace.replaceCalls)
	}
	got, err := filesystem.Read(t.Context(), "tracked.txt")
	if err != nil || string(got) != "before" {
		t.Fatalf("invalid replace mutated workspace: %q, %v", got, err)
	}
}

type replaceCountingWorkspace struct {
	tool.Workspace
	replaceCalls int
}

func (w *replaceCountingWorkspace) ReplaceFile(ctx context.Context, path string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	w.replaceCalls++
	return w.Workspace.ReplaceFile(ctx, path, old, data)
}
