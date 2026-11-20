package executionclient

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
	"github.com/stacklok/mecatl/internal/executionexecutor"
	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
	"github.com/stacklok/mecatl/internal/executorrpc/fileserver"
	"github.com/stacklok/mecatl/internal/executorrpc/memexecutor"
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
	return acquireRemoteRun(t, fx)
}

type recordingFiles struct {
	*fileserver.Server
	mu       sync.Mutex
	contexts []*executorv1.RequestContext
}

func (r *recordingFiles) StatFile(
	ctx context.Context,
	req *executorv1.StatFileRequest,
) (*executorv1.StatFileResponse, error) {
	r.mu.Lock()
	r.contexts = append(r.contexts, req.GetContext())
	r.mu.Unlock()
	return r.Server.StatFile(ctx, req)
}

func executorServicesEnvironment(
	t *testing.T,
	runner *memexecutor.Runner,
) (tool.Environment, *recordingFiles) {
	t.Helper()
	srv, err := fileserver.New(
		memfs.NewWorkspace(remoteRoot),
		fileserver.WithPathEscapeErrors(memfs.ErrPathEscape),
	)
	if err != nil {
		t.Fatal(err)
	}
	files := &recordingFiles{Server: srv}
	commands, err := commandserver.New(runner)
	if err != nil {
		t.Fatal(err)
	}
	fx := startFixture(t, &integrationBackend{}, nil, func(s *grpc.Server) {
		executorv1.RegisterFileServiceServer(s, files)
		executorv1.RegisterCommandServiceServer(s, commands)
	})
	t.Cleanup(fx.stop)
	return acquireRemoteRun(t, fx, WithExecutorServices()), files
}

func acquireRemoteRun(t *testing.T, fx grpcFixture, opts ...ProviderOption) tool.Environment {
	t.Helper()
	client, err := New(fx.endpoint, fx.clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	provider, err := NewTemplateProvider(client, "coding", codingRevision, opts...)
	if err != nil {
		t.Fatal(err)
	}
	principal := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	binding, err := provider.Bind(t.Context(), server.PlacementBindRequest{
		Selector:  server.DefaultPlacement(),
		Principal: principal,
		BindingID: "files",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	handle, err := provider.AcquireRun(t.Context(), server.ExecutionRunRequest{
		Ref:       binding.Ref,
		Principal: principal,
		BindingID: "files",
		RunID:     "run-files",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Release(context.Background()) })
	return handle.Environment()
}

var remoteTransports = map[string]func(t *testing.T) tool.Environment{
	"ProviderFiles": remoteExecutorEnvironment,
	"ExecutorServices": func(t *testing.T) tool.Environment {
		env, _ := executorServicesEnvironment(t, memexecutor.NewRunner())
		return env
	},
}

func TestRemoteWorkspaceConformanceThroughSignedGRPC(t *testing.T) {
	for name, environment := range remoteTransports {
		t.Run(name, func(t *testing.T) {
			factory := func(t *testing.T) tool.Workspace { return environment(t).Workspace() }
			fsconformance.Run(t, factory)
			fsconformance.RunNamespace(t, factory)
		})
	}
}

func TestExecutorServicesCarryTheRunClaim(t *testing.T) {
	runner := memexecutor.NewRunner()
	runner.Script("echo hi", memexecutor.Exit("hi\n", "", 0))
	env, files := executorServicesEnvironment(t, runner)
	if _, err := env.Workspace().Stat(t.Context(), "absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat = %v, want not found", err)
	}
	files.mu.Lock()
	got := files.contexts
	files.mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("server saw %d contexts, want 1", len(got))
	}
	rc := got[0]
	if rc.GetOwner().GetSubject() != "alice" || rc.GetBindingId() != "files" ||
		rc.GetRunId() != "run-files" || rc.GetClaimId() != "claim" || rc.GetEpoch() == 0 ||
		rc.GetGrantGeneration() != 1 || rc.GetEnvironment().GetId() == "" {
		t.Fatalf("request context = %v, want the run claim", rc)
	}
	res, err := env.CommandRunner().Run(t.Context(), "echo hi")
	if err != nil || res.Stdout != "hi\n" {
		t.Fatalf("Run = %+v, %v", res, err)
	}
}

func TestRemoteFileToolsPreserveLedgerAndNamespaceContracts(t *testing.T) {
	for name, environment := range remoteTransports {
		t.Run(name, func(t *testing.T) { testRemoteFileTools(t, environment(t)) })
	}
}

func testRemoteFileTools(t *testing.T, env tool.Environment) {
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
