// Package commandconformance checks a CommandService implementation through its gRPC client.
package commandconformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/durationpb"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
)

// Harness supplies the client and the commands that produce each scenario.
type Harness struct {
	Client func(t *testing.T) executorv1.CommandServiceClient
	// Succeed writes "out" to stdout and "err" to stderr, then exits 0.
	Succeed string
	// Fail exits with status 3.
	Fail string
	// Block writes "ready" to stdout, then runs until killed.
	Block string
	// BlockWithChild is Block with a background child that outlives its shell.
	BlockWithChild string
	// ChildGone returns nil once every BlockWithChild process has ended.
	ChildGone func(ctx context.Context) error
	// Survivor exits 0, leaving a process that survives the kill.
	Survivor string
}

const waitLimit = 30 * time.Second

// Run runs the suite.
func Run(t *testing.T, h Harness) {
	t.Run("ExitStatus", func(t *testing.T) { testExitStatus(t, h) })
	t.Run("StreamsBeforeExit", func(t *testing.T) { testStreamsBeforeExit(t, h) })
	t.Run("Timeout", func(t *testing.T) { testTimeout(t, h) })
	t.Run("HalfCloseKeepsRunning", func(t *testing.T) { testHalfCloseKeepsRunning(t, h) })
	t.Run("CloseKillsChildren", func(t *testing.T) { testCloseKillsChildren(t, h) })
	t.Run("OutcomeUnknown", func(t *testing.T) { testOutcomeUnknown(t, h) })
	t.Run("RejectsInvalidStart", func(t *testing.T) { testRejectsInvalidStart(t, h) })
}

type call struct {
	t      *testing.T
	stream executorv1.CommandService_RunCommandClient
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func start(
	ctx context.Context, t *testing.T, h Harness, command string, timeout time.Duration,
) *call {
	t.Helper()
	stream, err := h.Client(t).RunCommand(ctx)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	req := &executorv1.StartCommand{Command: command}
	if timeout > 0 {
		req.Timeout = durationpb.New(timeout)
	}
	if err := stream.Send(&executorv1.RunCommandRequest{
		Context: &executorv1.RequestContext{},
		Action:  &executorv1.RunCommandRequest_Start{Start: req},
	}); err != nil {
		t.Fatalf("send start: %v", err)
	}
	c := &call{t: t, stream: stream}
	msg, err := stream.Recv()
	if err != nil || msg.GetStarted() == nil {
		t.Fatalf("first event = %v, %v; want started", msg, err)
	}
	return c
}

// until reads output until stdout contains want or a result arrives.
func (c *call) until(want string) *executorv1.CommandResult {
	c.t.Helper()
	for {
		msg, err := c.stream.Recv()
		if err != nil {
			c.t.Fatalf("recv: %v", err)
		}
		switch ev := msg.GetEvent().(type) {
		case *executorv1.RunCommandResponse_Stdout:
			c.stdout.Write(ev.Stdout)
		case *executorv1.RunCommandResponse_Stderr:
			c.stderr.Write(ev.Stderr)
		case *executorv1.RunCommandResponse_Result:
			if _, err := c.stream.Recv(); !errors.Is(err, io.EOF) {
				c.t.Fatalf("after result: %v, want EOF", err)
			}
			return ev.Result
		default:
			c.t.Fatalf("unexpected event %v", msg)
		}
		if want != "" && bytes.Contains(c.stdout.Bytes(), []byte(want)) {
			return nil
		}
	}
}

func (c *call) result() *executorv1.CommandResult {
	c.t.Helper()
	return c.until("")
}

func wantState(t *testing.T, got *executorv1.CommandResult, want executorv1.CommandState) {
	t.Helper()
	if got.GetState() != want {
		t.Fatalf("state = %v, want %v", got.GetState(), want)
	}
}

func testExitStatus(t *testing.T, h Harness) {
	c := start(t.Context(), t, h, h.Succeed, 0)
	res := c.result()
	wantState(t, res, executorv1.CommandState_COMMAND_STATE_SUCCEEDED)
	if res.GetExitCode() != 0 || c.stdout.String() != "out" || c.stderr.String() != "err" {
		t.Fatalf("exit %d stdout %q stderr %q",
			res.GetExitCode(), c.stdout.String(), c.stderr.String())
	}
	c = start(t.Context(), t, h, h.Fail, 0)
	res = c.result()
	wantState(t, res, executorv1.CommandState_COMMAND_STATE_FAILED)
	if res.GetExitCode() != 3 {
		t.Fatalf("exit code = %d, want 3", res.GetExitCode())
	}
}

func testStreamsBeforeExit(t *testing.T, h Harness) {
	c := start(t.Context(), t, h, h.Block, 0)
	if res := c.until("ready"); res != nil {
		t.Fatalf("result %v before output", res)
	}
	if err := c.stream.Send(&executorv1.RunCommandRequest{
		Action: &executorv1.RunCommandRequest_Cancel{Cancel: &executorv1.CancelCommand{}},
	}); err != nil {
		t.Fatalf("send cancel: %v", err)
	}
	wantState(t, c.result(), executorv1.CommandState_COMMAND_STATE_CANCELLED)
}

func testTimeout(t *testing.T, h Harness) {
	c := start(t.Context(), t, h, h.Block, 200*time.Millisecond)
	wantState(t, c.result(), executorv1.CommandState_COMMAND_STATE_TIMED_OUT)
}

func testHalfCloseKeepsRunning(t *testing.T, h Harness) {
	c := start(t.Context(), t, h, h.Block, 500*time.Millisecond)
	if err := c.stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
	wantState(t, c.result(), executorv1.CommandState_COMMAND_STATE_TIMED_OUT)
}

func testCloseKillsChildren(t *testing.T, h Harness) {
	if h.BlockWithChild == "" || h.ChildGone == nil {
		t.Skip("harness has no background-child scenario")
	}
	ctx, cancel := context.WithCancel(t.Context())
	c := start(ctx, t, h, h.BlockWithChild, 0)
	if res := c.until("ready"); res != nil {
		t.Fatalf("result %v before output", res)
	}
	cancel()
	if _, err := c.stream.Recv(); status.Code(err) != codes.Canceled {
		t.Fatalf("recv after close: %v, want Canceled", err)
	}
	waitCtx, waitCancel := context.WithTimeout(t.Context(), waitLimit)
	defer waitCancel()
	if err := h.ChildGone(waitCtx); err != nil {
		t.Fatalf("background child survived closing the call: %v", err)
	}
}

func testOutcomeUnknown(t *testing.T, h Harness) {
	if h.Survivor == "" {
		t.Skip("harness has no survivor scenario")
	}
	c := start(t.Context(), t, h, h.Survivor, 0)
	wantState(t, c.result(), executorv1.CommandState_COMMAND_STATE_OUTCOME_UNKNOWN)
}

func testRejectsInvalidStart(t *testing.T, h Harness) {
	startWith := func(s *executorv1.StartCommand) *executorv1.RunCommandRequest {
		return &executorv1.RunCommandRequest{Action: &executorv1.RunCommandRequest_Start{Start: s}}
	}
	for name, req := range map[string]*executorv1.RunCommandRequest{
		"cancel first": {Action: &executorv1.RunCommandRequest_Cancel{
			Cancel: &executorv1.CancelCommand{},
		}},
		"empty": startWith(&executorv1.StartCommand{}),
		"negative timeout": startWith(&executorv1.StartCommand{
			Command: h.Succeed, Timeout: durationpb.New(-time.Second),
		}),
	} {
		t.Run(name, func(t *testing.T) {
			stream, err := h.Client(t).RunCommand(t.Context())
			if err != nil {
				t.Fatalf("RunCommand: %v", err)
			}
			if err := stream.Send(req); err != nil {
				t.Fatalf("send: %v", err)
			}
			if msg, err := stream.Recv(); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("recv = %v, %v; want InvalidArgument", msg, err)
			}
		})
	}
}

// Dial serves srv on an in-process listener for the test's lifetime and returns its client.
func Dial(t *testing.T, srv executorv1.CommandServiceServer) executorv1.CommandServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	executorv1.RegisterCommandServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return executorv1.NewCommandServiceClient(conn)
}
