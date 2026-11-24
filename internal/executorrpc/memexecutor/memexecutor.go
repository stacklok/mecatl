// Package memexecutor is a test-only executor over memfs and scripted commands.
package memexecutor

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
	"github.com/stacklok/mecatl/internal/executorrpc/fileserver"
)

// Executor configures NewClient; a nil service stays unregistered.
type Executor struct {
	Files          *memfs.Workspace
	FileOptions    []fileserver.Option
	Commands       *Runner
	CommandOptions []commandserver.Option
}

// NewClient serves e in process for the test's lifetime.
func NewClient(t testing.TB, e Executor) *grpc.ClientConn {
	t.Helper()
	gs := grpc.NewServer()
	if e.Files != nil {
		opts := append(
			[]fileserver.Option{fileserver.WithPathEscapeErrors(memfs.ErrPathEscape)},
			e.FileOptions...,
		)
		srv, err := fileserver.New(e.Files, opts...)
		if err != nil {
			t.Fatal(err)
		}
		executorv1.RegisterFileServiceServer(gs, srv)
	}
	if e.Commands != nil {
		srv, err := commandserver.New(e.Commands, e.CommandOptions...)
		if err != nil {
			t.Fatal(err)
		}
		executorv1.RegisterCommandServiceServer(gs, srv)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///memexecutor",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
