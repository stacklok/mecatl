package executorclient_test

import (
	"bytes"
	"context"
	"errors"
	"net"
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
	stderr = func(s string) *executorv1.RunCommandResponse {
		return &executorv1.RunCommandResponse{
			Event: &executorv1.RunCommandResponse_Stderr{Stderr: []byte(s)},
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

func refusal(code codes.Code, why executorv1.ErrorReason) func(commandStream) error {
	return func(commandStream) error {
		st, err := status.New(code, "refused").WithDetails(&executorv1.ErrorDetail{Reason: why})
		if err != nil {
			return err
		}
		return st.Err()
	}
}

func TestFailureBeforeStartedIsUnknownUnlessRefused(t *testing.T) {
	const (
		none    = executorv1.ErrorReason_ERROR_REASON_UNSPECIFIED
		limit   = executorv1.ErrorReason_ERROR_REASON_LIMIT_EXCEEDED
		noShell = executorv1.ErrorReason_ERROR_REASON_NO_SHELL
		failed  = executorv1.ErrorReason_ERROR_REASON_START_FAILED
		refused = executorv1.ErrorReason_ERROR_REASON_NOT_FORWARDED
	)
	cases := map[string]struct {
		run     func(commandStream) error
		unknown bool
	}{
		"unavailable": {refusal(codes.Unavailable, none), true},
		"raw send error": {func(commandStream) error {
			return errors.New("send started: broken pipe")
		}, true},
		"clean end": {func(commandStream) error { return nil }, true},
		"output first": {func(s commandStream) error {
			return send(s, stdout("x"), started, result(false))
		}, true},
		"untagged internal":   {refusal(codes.Internal, none), true},
		"untagged exhausted":  {refusal(codes.ResourceExhausted, none), true},
		"refused":             {refusal(codes.InvalidArgument, none), false},
		"command over limit":  {refusal(codes.ResourceExhausted, limit), false},
		"no shell":            {refusal(codes.FailedPrecondition, noShell), false},
		"start failed tagged": {refusal(codes.Internal, failed), false},
		"untagged denied":     {refusal(codes.PermissionDenied, none), true},
		"denied unforwarded":  {refusal(codes.PermissionDenied, refused), false},
		"unforwarded unready": {refusal(codes.Unavailable, refused), false},
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
	const marker = "\n... [output truncated]"
	cases := map[string]struct {
		run            func(commandStream) error
		stdout, stderr string
	}{
		"client cap": {func(s commandStream) error {
			return send(s, started, stdout("aaaa"), stdout("bbbb"), stdout("cccc"), result(false))
		}, "aaaabb" + marker, ""},
		"server flag": {func(s commandStream) error {
			return send(s, started, stdout("ok"), result(true))
		}, "ok" + marker, ""},
		"stderr only": {func(s commandStream) error {
			return send(s, started, stderr("eeeeeeee"), result(false))
		}, "", "eeeeee" + marker},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			conn := dialScripted(t, c.run)
			r := executorclient.NewRunner(conn, root, nil, executorclient.WithMaxOutputBytes(6))
			res, err := r.Run(t.Context(), "cmd")
			if err != nil || res.Stdout != c.stdout || res.Stderr != c.stderr {
				t.Fatalf("Run = %q, %q, %v; want %q, %q", res.Stdout, res.Stderr, err,
					c.stdout, c.stderr)
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
