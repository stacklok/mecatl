package executorclient_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/internal/executorrpc/executorclient"
)

type commandStream = grpc.BidiStreamingServer[
	executorv1.RunCommandRequest,
	executorv1.RunCommandResponse,
]

type scriptedCommands struct {
	executorv1.UnimplementedCommandServiceServer
	run func(commandStream) error
}

func (s scriptedCommands) RunCommand(stream commandStream) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	return s.run(stream)
}

func dialScripted(
	t *testing.T,
	run func(commandStream) error,
) grpc.ClientConnInterface {
	t.Helper()
	gs := grpc.NewServer()
	executorv1.RegisterCommandServiceServer(gs, scriptedCommands{run: run})
	lis := bufconn.Listen(1 << 20)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := executorclient.Dial(
		"passthrough:///scripted",
		insecure.NewCredentials(),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func send(stream commandStream, events ...*executorv1.RunCommandResponse) error {
	for _, ev := range events {
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	return nil
}

var (
	started = &executorv1.RunCommandResponse{
		Event: &executorv1.RunCommandResponse_Started{Started: &executorv1.CommandStarted{}},
	}
	stdout = func(s string) *executorv1.RunCommandResponse {
		return &executorv1.RunCommandResponse{
			Event: &executorv1.RunCommandResponse_Stdout{Stdout: []byte(s)},
		}
	}
	result = func(truncated bool) *executorv1.RunCommandResponse {
		return &executorv1.RunCommandResponse{Event: &executorv1.RunCommandResponse_Result{
			Result: &executorv1.CommandResult{
				State:           executorv1.CommandState_COMMAND_STATE_SUCCEEDED,
				OutputTruncated: truncated,
			},
		}}
	}
)

func TestFailureBeforeStartedIsUnknownUnlessRefused(t *testing.T) {
	cases := map[string]struct {
		run     func(commandStream) error
		unknown bool
	}{
		"unavailable": {func(commandStream) error {
			return status.Error(codes.Unavailable, "gone")
		}, true},
		"raw send error": {func(commandStream) error {
			return errors.New("send started: broken pipe")
		}, true},
		"clean end": {func(commandStream) error { return nil }, true},
		"output first": {func(s commandStream) error {
			return send(s, stdout("x"), started, result(false))
		}, true},
		"refused": {func(commandStream) error {
			return status.Error(codes.InvalidArgument, "command is required")
		}, false},
		"start failed": {func(commandStream) error {
			return status.Error(codes.Internal, "command did not start")
		}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := executorclient.NewRunner(dialScripted(t, c.run), root, nil)
			_, err := r.Run(t.Context(), "cmd")
			if err == nil || errors.Is(err, executorclient.ErrOutcomeUnknown) != c.unknown {
				t.Fatalf("Run = %v, want unknown=%v", err, c.unknown)
			}
		})
	}
}

func TestOutputIsCappedAndTruncationReported(t *testing.T) {
	cases := map[string]struct {
		run  func(commandStream) error
		want string
	}{
		"client cap": {func(s commandStream) error {
			return send(s, started, stdout("aaaa"), stdout("bbbb"), stdout("cccc"), result(false))
		}, "aaaabb"},
		"server flag": {func(s commandStream) error {
			return send(s, started, stdout("ok"), result(true))
		}, "ok"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			conn := dialScripted(t, c.run)
			r := executorclient.NewRunner(conn, root, nil, executorclient.WithMaxOutputBytes(6))
			res, err := r.Run(t.Context(), "cmd")
			if err != nil {
				t.Fatal(err)
			}
			got, ok := strings.CutSuffix(res.Stdout, "\n... [output truncated by the executor]")
			if !ok || got != c.want {
				t.Fatalf("stdout = %q, want %q with the truncation marker", res.Stdout, c.want)
			}
		})
	}
}

func TestReadOverClientLimitFails(t *testing.T) {
	files := memfs.NewWorkspace(root)
	f := serve(t, files, nil)
	ws := executorclient.NewWorkspace(f.conn, root, nil, executorclient.WithMaxFileBytes(8))
	for name, size := range map[string]int{"fits": 8, "over": 9} {
		if _, err := ws.CreateFile(t.Context(), name, bytes.Repeat([]byte("x"), size)); err != nil {
			t.Fatal(err)
		}
	}
	if data, err := ws.Read(t.Context(), "fits"); err != nil || len(data) != 8 {
		t.Fatalf("Read(fits) = %d bytes, %v", len(data), err)
	}
	if _, err := ws.Read(t.Context(), "over"); !errors.Is(err, executorclient.ErrLimitExceeded) {
		t.Fatalf("Read(over) = %v, want ErrLimitExceeded", err)
	}
}
