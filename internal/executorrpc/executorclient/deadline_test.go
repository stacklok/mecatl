package executorclient_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/executorrpc/executorclient"
	"github.com/stacklok/mecatl/internal/executorrpc/fileconformance"
	"github.com/stacklok/mecatl/internal/executorrpc/fileserver"
)

const fileCallTimeout = 50 * time.Millisecond

// stuckFiles never answers ReadFile, CreateFile or RemoveFile and ignores
// their contexts, as a handler blocked in a syscall on hung storage does.
type stuckFiles struct {
	executorv1.FileServiceServer
	release chan struct{}
}

func (s stuckFiles) ReadFile(
	_ *executorv1.ReadFileRequest,
	stream grpc.ServerStreamingServer[executorv1.ReadFileResponse],
) error {
	if err := stream.Send(&executorv1.ReadFileResponse{Data: []byte("partial")}); err != nil {
		return err
	}
	<-s.release
	return nil
}

func (s stuckFiles) CreateFile(
	grpc.ClientStreamingServer[executorv1.CreateFileRequest, executorv1.CreateFileResponse],
) error {
	<-s.release
	return nil
}

func (s stuckFiles) RemoveFile(
	context.Context,
	*executorv1.RemoveFileRequest,
) (*executorv1.RemoveFileResponse, error) {
	<-s.release
	return &executorv1.RemoveFileResponse{}, nil
}

func dialStuck(t *testing.T, opts ...executorclient.Option) *executorclient.Workspace {
	t.Helper()
	files, err := fileserver.New(memfs.NewWorkspace(root))
	if err != nil {
		t.Fatal(err)
	}
	stuck := stuckFiles{FileServiceServer: files, release: make(chan struct{})}
	conn := fileconformance.NewClient(t, stuck)
	t.Cleanup(func() { close(stuck.release) })
	return executorclient.NewWorkspace(conn, root, nil, opts...)
}

func TestFileCallFailsWhenStorageDoesNotAnswer(t *testing.T) {
	ws := dialStuck(t, executorclient.WithFileCallTimeout(fileCallTimeout))
	calls := map[string]func(context.Context) error{
		"read": func(ctx context.Context) error {
			_, err := ws.Read(ctx, "f")
			return err
		},
		"create": func(ctx context.Context) error {
			_, err := ws.CreateFile(ctx, "f", []byte("x"))
			return err
		},
		"remove": func(ctx context.Context) error { return ws.Remove(ctx, "f") },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			err := call(ctx)
			if !errors.Is(err, executorclient.ErrStorageTimeout) {
				t.Fatalf("err = %v, want ErrStorageTimeout", err)
			}
			if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				t.Fatalf("err = %v, ctx = %v: reported as the caller's deadline", err, ctx.Err())
			}
			if !strings.Contains(err.Error(), "a write may still have completed") {
				t.Fatalf("err = %v, want it to warn about the write", err)
			}
		})
	}
}

func TestCallerDeadlineIsNotAStorageTimeout(t *testing.T) {
	ws := dialStuck(t)
	ctx, cancel := context.WithTimeout(t.Context(), fileCallTimeout)
	defer cancel()
	_, err := ws.Read(ctx, "f")
	timedOut := errors.Is(err, executorclient.ErrStorageTimeout)
	if !errors.Is(err, context.DeadlineExceeded) || timedOut {
		t.Fatalf("err = %v, want only context.DeadlineExceeded", err)
	}

	ctx, cancel = context.WithCancel(t.Context())
	time.AfterFunc(fileCallTimeout, cancel)
	err = ws.Remove(ctx, "f")
	timedOut = errors.Is(err, executorclient.ErrStorageTimeout)
	if !errors.Is(err, context.Canceled) || timedOut {
		t.Fatalf("err = %v, want only context.Canceled", err)
	}
}

func TestStorageTimeoutIsAToolError(t *testing.T) {
	ws := dialStuck(t, executorclient.WithFileCallTimeout(fileCallTimeout))
	env := tool.MustEnvironment(session.EnvironmentRef{}, ws, memledger.New(), nil)
	call := session.ToolCall{ID: "1", Name: "Write", Args: []byte(`{"path": "f", "content": "x"}`)}
	res, err := fstools.WriteTool{}.Execute(t.Context(), call, env)
	if err == nil && !res.IsError {
		t.Fatalf("write to hung storage succeeded: %q", res.Content)
	}
	if !errors.Is(err, executorclient.ErrStorageTimeout) {
		t.Fatalf("err = %v, want ErrStorageTimeout", err)
	}
}
