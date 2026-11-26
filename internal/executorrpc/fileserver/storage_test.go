package fileserver_test

import (
	"context"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/executorrpc/fileconformance"
	"github.com/stacklok/mecatl/internal/executorrpc/fileserver"
)

func wantCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if got := status.Code(unwrapStatus(err)); got != code {
		t.Fatalf("err = %v, want %v", err, code)
	}
}

// fullWorkspace fails every write with ENOSPC, as a full volume does.
type fullWorkspace struct{ *memfs.Workspace }

func (fullWorkspace) CreateFile(context.Context, string, []byte) (tool.FileVersion, error) {
	return tool.FileVersion{}, &fs.PathError{Op: "write", Path: "f", Err: syscall.ENOSPC}
}

func (fullWorkspace) ReplaceFile(
	context.Context,
	string,
	tool.FileVersion,
	[]byte,
) (tool.FileVersion, error) {
	return tool.FileVersion{}, &fs.PathError{Op: "write", Path: "f", Err: syscall.EDQUOT}
}

func TestFullStorageIsAToolError(t *testing.T) {
	full := fullWorkspace{memfs.NewWorkspace(root)}
	srv := newServer(
		t,
		full,
		fileserver.WithStorageFullErrors(syscall.ENOSPC, syscall.EDQUOT),
	)
	ws := fileconformance.NewWorkspace(fileconformance.NewClient(t, srv), root)
	ctx := t.Context()

	_, err := ws.ReplaceFile(ctx, "f", tool.FileVersion{}, []byte("x"))
	wantCode(t, err, codes.ResourceExhausted)

	env := tool.MustEnvironment(session.EnvironmentRef{}, ws, memledger.New(), nil)
	call := session.ToolCall{
		ID:   "1",
		Name: "Write",
		Args: []byte(`{"path": "f", "content": "x"}`),
	}
	res, err := fstools.WriteTool{}.Execute(ctx, call, env)
	if err == nil && !res.IsError {
		t.Fatalf("write to full storage succeeded: %q", res.Content)
	}
	wantCode(t, err, codes.ResourceExhausted)
	if !strings.Contains(err.Error(), "storage is full") {
		t.Fatalf("err = %v, want it to name full storage", err)
	}
}
