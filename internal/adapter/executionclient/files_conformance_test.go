package executionclient

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
	"github.com/stacklok/mecatl/internal/executionexecutor"
)

type executorBackend struct {
	integrationBackend
	executor *executionexecutor.Executor
}

func (b *executorBackend) File(ctx context.Context, _, _ string, q executionenv.FileRequest) (executionenv.FileResponse, error) {
	r, err := b.executor.Execute(ctx, executionenv.ExecutorRequest{Operation: q.Operation, Path: q.Path, Destination: q.Destination, Pattern: q.Pattern, Data: q.Data, Version: q.Version, Limit: q.Limit})
	return r.FileResponse, err
}

func remoteExecutorEnvironment(t *testing.T) tool.Environment {
	t.Helper()
	x, err := executionexecutor.New(t.TempDir(), executionexecutor.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = x.Close() })
	fx := startFixture(t, &executorBackend{executor: x}, nil)
	t.Cleanup(fx.stop)
	client, err := New(fx.endpoint, fx.clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	provider, err := NewProvider(client, "coding")
	if err != nil {
		t.Fatal(err)
	}
	principal := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	binding, err := provider.Bind(t.Context(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Principal: principal, BindingID: "files"})
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	handle, err := provider.AcquireRun(t.Context(), server.ExecutionRunRequest{Ref: binding.Ref, Principal: principal, BindingID: "files", RunID: "run-files"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Release(context.Background()) })
	return handle.Environment()
}

func TestRemoteWorkspaceConformanceThroughSignedGRPC(t *testing.T) {
	factory := func(t *testing.T) tool.Workspace { return remoteExecutorEnvironment(t).Workspace() }
	fsconformance.Run(t, factory)
	fsconformance.RunNamespace(t, factory)
}

func TestRemoteFileToolsPreserveLedgerAndNamespaceContracts(t *testing.T) {
	env := remoteExecutorEnvironment(t)
	ctx := t.Context()
	call := func(body tool.Tool, args string, wantError bool, contains string) {
		t.Helper()
		result, err := body.Execute(ctx, session.ToolCall{ID: "file", Name: body.Spec().Name, Args: json.RawMessage(args)}, env)
		if err != nil || result.IsError != wantError || !strings.Contains(result.Content, contains) {
			t.Fatalf("%s: result=%+v error=%v", body.Spec().Name, result, err)
		}
	}
	if _, err := env.Workspace().CreateFile(ctx, "main.go", []byte("package main\n// before\n")); err != nil {
		t.Fatal(err)
	}
	call(fstools.EditTool{}, `{"path":"main.go","old_string":"before","new_string":"after"}`, true, "")
	call(fstools.WriteTool{}, `{"path":"main.go","content":"clobber"}`, true, "")
	call(fstools.ReadTool{}, `{"path":"main.go"}`, false, "before")
	call(fstools.EditTool{}, `{"path":"main.go","old_string":"before","new_string":"after"}`, false, "")
	call(fstools.ReadTool{}, `{"path":"main.go"}`, false, "after")
	// A mutation outside the read ledger invalidates both Edit and existing Write.
	_, version, err := env.Workspace().ReadVersion(ctx, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Workspace().ReplaceFile(ctx, "main.go", version, []byte("package main\n// concurrent\n")); err != nil {
		t.Fatal(err)
	}
	call(fstools.EditTool{}, `{"path":"main.go","old_string":"concurrent","new_string":"lost"}`, true, "")
	call(fstools.WriteTool{}, `{"path":"main.go","content":"lost"}`, true, "")
	call(fstools.ReadTool{}, `{"path":"main.go"}`, false, "concurrent")
	call(fstools.WriteTool{}, `{"path":"main.go","content":"package main\n// final\n"}`, false, "")
	call(fstools.ListDirTool{}, `{"path":"."}`, false, "main.go")
	call(fstools.GrepTool{}, `{"path":"*.go","pattern":"final"}`, false, "final")
	call(fstools.GlobTool{}, `{"pattern":"**/*.go"}`, false, "main.go")
	call(fstools.WriteTool{}, `{"path":"fresh.go","content":"package main\n"}`, false, "")
	// Seed without Write's automatic ledger update: namespace operations need no ledger.
	if _, err := env.Workspace().CreateFile(ctx, "unread.go", []byte("package unread\n")); err != nil {
		t.Fatal(err)
	}
	call(fstools.CopyTool{}, `{"source":"unread.go","destination":"copied.go"}`, false, "")
	call(fstools.MoveTool{}, `{"source":"copied.go","destination":"moved.go"}`, false, "")
	data, err := env.Workspace().Read(ctx, "moved.go")
	if err != nil || string(data) != "package unread\n" {
		t.Fatalf("moved=%q error=%v", data, err)
	}
	call(fstools.CopyTool{}, `{"source":"unread.go","destination":"main.go"}`, true, "")
	call(fstools.MoveTool{}, `{"source":"moved.go","destination":"main.go"}`, true, "")
	call(fstools.RemoveTool{}, `{"path":"moved.go"}`, false, "")
	if _, err := env.Workspace().Stat(ctx, "moved.go"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("removed file: %v", err)
	}
	data, err = env.Workspace().Read(ctx, "main.go")
	if err != nil || string(data) != "package main\n// final\n" {
		t.Fatalf("final=%q error=%v", data, err)
	}
}
