//go:build kind_execution_e2e

package k8s_execution_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/executionclient"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func qualifyRemoteFileTools(t *testing.T, ctx context.Context, client *executionclient.Client, owner executionenv.Owner, binding string, ref executionenv.EnvironmentRef) {
	t.Helper()
	provider, err := executionclient.NewProvider(client, "go")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := provider.AcquireRun(ctx, server.ExecutionRunRequest{Ref: session.EnvironmentRef{Kind: "kubernetes", ID: ref.ID, Revision: ref.Revision}, Principal: &session.Principal{Issuer: owner.Issuer, Subject: owner.Subject, GrantType: session.GrantTypeUser}, BindingID: session.SessionID(binding), RunID: "file-tool-matrix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := handle.Release(ctx); err != nil {
			t.Error(err)
		}
	}()
	env := handle.Environment()
	call := func(body tool.Tool, args string, wantError bool, contains string) {
		t.Helper()
		result, err := body.Execute(ctx, session.ToolCall{ID: "matrix", Name: body.Spec().Name, Args: json.RawMessage(args)}, env)
		if err != nil || result.IsError != wantError || !strings.Contains(result.Content, contains) {
			t.Fatalf("%s: result=%+v err=%v", body.Spec().Name, result, err)
		}
	}
	if _, err := env.Workspace().CreateFile(ctx, "matrix/source.go", []byte("package matrix\n// before\n")); err != nil {
		t.Fatal(err)
	}
	call(fstools.EditTool{}, `{"path":"matrix/source.go","old_string":"before","new_string":"after"}`, true, "")
	call(fstools.WriteTool{}, `{"path":"matrix/source.go","content":"clobber"}`, true, "")
	call(fstools.CopyTool{}, `{"source":"matrix/source.go","destination":"matrix/copied.go"}`, false, "")
	call(fstools.MoveTool{}, `{"source":"matrix/copied.go","destination":"matrix/moved.go"}`, false, "")
	call(fstools.RemoveTool{}, `{"path":"matrix/moved.go"}`, false, "")
	call(fstools.ReadTool{}, `{"path":"matrix/source.go"}`, false, "before")
	call(fstools.EditTool{}, `{"path":"matrix/source.go","old_string":"before","new_string":"after"}`, false, "")
	call(fstools.WriteTool{}, `{"path":"matrix/source.go","content":"package matrix\n// final\n"}`, false, "")
	call(fstools.WriteTool{}, `{"path":"matrix/new.go","content":"package matrix\n"}`, false, "")
	call(fstools.ListDirTool{}, `{"path":"matrix"}`, false, "source.go")
	call(fstools.GrepTool{}, `{"path":"matrix/*.go","pattern":"final"}`, false, "final")
	call(fstools.GlobTool{}, `{"pattern":"matrix/*.go"}`, false, "source.go")
	data, err := env.Workspace().Read(ctx, "matrix/source.go")
	if err != nil || string(data) != "package matrix\n// final\n" {
		t.Fatalf("persisted matrix=%q err=%v", data, err)
	}
}
