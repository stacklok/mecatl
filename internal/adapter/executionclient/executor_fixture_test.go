package executionclient

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
)

const (
	opFileRead               = "file.read"
	opFileResolveAuthority   = "file.resolve_authority"
	opCommandStart           = "command.start"
	executorRemoteRootTarget = remoteRoot + "/main.go"
)

// serveExecutor serves register's services in memory, as one executor Pod would.
func serveExecutor(t *testing.T, register func(*grpc.Server)) grpc.ClientConnInterface {
	t.Helper()
	gs := grpc.NewServer()
	register(gs)
	lis := bufconn.Listen(1 << 20)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient(
		"passthrough:///executor",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

type executorCall struct {
	Operation string
	Command   string
}

// recordingExecutor answers reads and commands with fixed content and records
// each call; with executed set, it reports each call there and waits for release.
type recordingExecutor struct {
	executorv1.UnimplementedFileServiceServer
	executorv1.UnimplementedCommandServiceServer
	data, version  []byte
	stdout, stderr string
	mu             sync.Mutex
	operations     []string
	executed       chan executorCall
	release        <-chan struct{}
}

func (e *recordingExecutor) conn(t *testing.T) grpc.ClientConnInterface {
	t.Helper()
	return serveExecutor(t, func(s *grpc.Server) {
		executorv1.RegisterFileServiceServer(s, e)
		executorv1.RegisterCommandServiceServer(s, e)
	})
}

func (e *recordingExecutor) record(ctx context.Context, call executorCall) error {
	e.mu.Lock()
	e.operations = append(e.operations, call.Operation)
	e.mu.Unlock()
	if e.executed == nil {
		return nil
	}
	select {
	case e.executed <- call:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-e.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func orDefault[T ~[]byte | ~string](v T, fallback T) T {
	if len(v) == 0 {
		return fallback
	}
	return v
}

func (e *recordingExecutor) ResolveAuthorityPath(
	ctx context.Context,
	_ *executorv1.ResolveAuthorityPathRequest,
) (*executorv1.ResolveAuthorityPathResponse, error) {
	if err := e.record(ctx, executorCall{Operation: opFileResolveAuthority}); err != nil {
		return nil, err
	}
	return &executorv1.ResolveAuthorityPathResponse{
		Target:    executorRemoteRootTarget,
		Workspace: remoteRoot,
	}, nil
}

func (e *recordingExecutor) ReadFile(
	_ *executorv1.ReadFileRequest,
	stream grpc.ServerStreamingServer[executorv1.ReadFileResponse],
) error {
	if err := e.record(stream.Context(), executorCall{Operation: opFileRead}); err != nil {
		return err
	}
	return stream.Send(&executorv1.ReadFileResponse{
		Data:    orDefault(e.data, []byte("package main\n")),
		Version: &executorv1.FileVersion{Token: orDefault(e.version, []byte("v1"))},
	})
}

func (e *recordingExecutor) RunCommand(
	stream grpc.BidiStreamingServer[executorv1.RunCommandRequest, executorv1.RunCommandResponse],
) error {
	first, err := stream.Recv()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	call := executorCall{Operation: opCommandStart, Command: first.GetStart().GetCommand()}
	if err := e.record(stream.Context(), call); err != nil {
		return err
	}
	for _, event := range []*executorv1.RunCommandResponse{
		{Event: &executorv1.RunCommandResponse_Started{Started: &executorv1.CommandStarted{}}},
		{
			Event: &executorv1.RunCommandResponse_Stdout{
				Stdout: []byte(orDefault(e.stdout, "out\xff")),
			},
		},
		{
			Event: &executorv1.RunCommandResponse_Stderr{
				Stderr: []byte(orDefault(e.stderr, "err\xe2")),
			},
		},
		{Event: &executorv1.RunCommandResponse_Result{Result: &executorv1.CommandResult{
			State: executorv1.CommandState_COMMAND_STATE_SUCCEEDED,
		}}},
	} {
		if err := stream.Send(event); err != nil {
			return err
		}
	}
	return nil
}
