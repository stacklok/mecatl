// Package fileconformance checks a FileService implementation through the executor client.
package fileconformance

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/executorrpc/executorclient"
)

// Target is a fresh service and another actor on the files it serves.
type Target struct {
	Conn     grpc.ClientConnInterface
	Root     string
	External fsconformance.ExternalAccess
	// Context is sent with every call; nil sends an empty one.
	Context executorclient.RequestContextFunc
}

// Run runs the Workspace conformance suites through the executor client.
func Run(t *testing.T, newTarget func(t *testing.T) Target) {
	fixture := func(t *testing.T) (tool.Workspace, fsconformance.ExternalAccess) {
		target := newTarget(t)
		ws := executorclient.NewWorkspace(target.Conn, target.Root, target.Context)
		return ws, target.External
	}
	workspace := func(t *testing.T) tool.Workspace {
		ws, _ := fixture(t)
		return ws
	}
	t.Run("Workspace", func(t *testing.T) { fsconformance.Run(t, workspace) })
	t.Run("Namespace", func(t *testing.T) { fsconformance.RunNamespace(t, workspace) })
	t.Run("MutationAffinity", func(t *testing.T) { fsconformance.RunMutationAffinity(t, fixture) })
	t.Run("ExternalMutationConflict", func(t *testing.T) {
		fsconformance.RunExternalMutationConflict(t, fixture)
	})
}

// Dial serves srv in memory and returns a connection to it.
func Dial(t *testing.T, srv executorv1.FileServiceServer) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	executorv1.RegisterFileServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	conn, err := executorclient.Dial(
		"passthrough:///bufnet",
		insecure.NewCredentials(),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// ChunkBytes is the client's write chunk size.
const ChunkBytes = executorclient.ChunkBytes

// NewWorkspace returns the client's Workspace with an empty request context.
func NewWorkspace(conn grpc.ClientConnInterface, root string) *executorclient.Workspace {
	return executorclient.NewWorkspace(conn, root, nil)
}

// Reason returns the ErrorReason in err.
func Reason(err error) executorv1.ErrorReason {
	for _, d := range status.Convert(err).Details() {
		if detail, ok := d.(*executorv1.ErrorDetail); ok {
			return detail.GetReason()
		}
	}
	return executorv1.ErrorReason_ERROR_REASON_UNSPECIFIED
}
