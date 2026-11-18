package memexecutor_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/executorrpc/commandconformance"
	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
	"github.com/stacklok/mecatl/internal/executorrpc/memexecutor"
)

func commandClient(t *testing.T, r *memexecutor.Runner) executorv1.CommandServiceClient {
	conn := memexecutor.Dial(t, memexecutor.Executor{Commands: r})
	return executorv1.NewCommandServiceClient(conn)
}

func waitIdle(ctx context.Context, r *memexecutor.Runner) error {
	for r.Running() != 0 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

func TestCommandServiceConformance(t *testing.T) {
	r := memexecutor.NewRunner()
	r.Script("succeed", memexecutor.Exit("out", "err", 0))
	r.Script("fail", memexecutor.Exit("", "", 3))
	r.Script("block", memexecutor.Block("ready"))
	r.Script("survivor", memexecutor.OutcomeUnknown(memexecutor.Exit("", "", 0)))
	client := commandClient(t, r)
	commandconformance.Run(t, commandconformance.Harness{
		Client:         func(*testing.T) executorv1.CommandServiceClient { return client },
		Succeed:        "succeed",
		Fail:           "fail",
		Block:          "block",
		BlockWithChild: "block",
		ChildGone:      func(ctx context.Context) error { return waitIdle(ctx, r) },
		Survivor:       "survivor",
	})
}

func startCommand(
	ctx context.Context,
	t *testing.T,
	client executorv1.CommandServiceClient,
	command string,
) executorv1.CommandService_RunCommandClient {
	t.Helper()
	stream, err := client.RunCommand(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&executorv1.RunCommandRequest{Action: &executorv1.RunCommandRequest_Start{
		Start: &executorv1.StartCommand{Command: command},
	}}); err != nil {
		t.Fatal(err)
	}
	if msg, err := stream.Recv(); err != nil || msg.GetStarted() == nil {
		t.Fatalf("first event = %v, %v; want started", msg, err)
	}
	return stream
}

func recvStdout(t *testing.T, stream executorv1.CommandService_RunCommandClient) string {
	t.Helper()
	msg, err := stream.Recv()
	if err != nil || msg.GetStdout() == nil {
		t.Fatalf("event = %v, %v; want stdout", msg, err)
	}
	return string(msg.GetStdout())
}

func recvResult(
	t *testing.T,
	stream executorv1.CommandService_RunCommandClient,
) *executorv1.CommandResult {
	t.Helper()
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if res := msg.GetResult(); res != nil {
			if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
				t.Fatalf("after result: %v, want EOF", err)
			}
			return res
		}
	}
}

func sendCancel(t *testing.T, stream executorv1.CommandService_RunCommandClient) {
	t.Helper()
	if err := stream.Send(&executorv1.RunCommandRequest{
		Action: &executorv1.RunCommandRequest_Cancel{Cancel: &executorv1.CancelCommand{}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStreamDeliversEachChunkBeforeExit(t *testing.T) {
	next := make(chan struct{})
	r := memexecutor.NewRunner()
	r.Script("stream", memexecutor.Stream(next, "one", "two"))
	stream := startCommand(t.Context(), t, commandClient(t, r), "stream")
	for _, want := range []string{"one", "two"} {
		next <- struct{}{}
		if got := recvStdout(t, stream); got != want {
			t.Fatalf("stdout = %q, want %q", got, want)
		}
	}
	res := recvResult(t, stream)
	if res.GetState() != executorv1.CommandState_COMMAND_STATE_SUCCEEDED {
		t.Fatalf("result = %v, want succeeded", res)
	}
}

func TestClosingTheCallEndsTheScript(t *testing.T) {
	ended := make(chan error, 1)
	r := memexecutor.NewRunner()
	block := memexecutor.Block("ready")
	r.Script("block", func(ctx context.Context, stdout, stderr io.Writer) commandserver.Exit {
		exit := block(ctx, stdout, stderr)
		ended <- ctx.Err()
		return exit
	})
	ctx, cancel := context.WithCancel(t.Context())
	stream := startCommand(ctx, t, commandClient(t, r), "block")
	if got := recvStdout(t, stream); got != "ready" {
		t.Fatalf("stdout = %q, want ready", got)
	}
	cancel()
	if _, err := stream.Recv(); status.Code(err) != codes.Canceled {
		t.Fatalf("recv after close: %v, want Canceled", err)
	}
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("script context ended with %v, want Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("script still running after the call closed")
	}
	waitCtx, waitCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer waitCancel()
	if err := waitIdle(waitCtx, r); err != nil {
		t.Fatalf("runner still busy: %v", err)
	}
}

func TestOutcomeUnknown(t *testing.T) {
	r := memexecutor.NewRunner()
	r.Script("exit", memexecutor.OutcomeUnknown(memexecutor.Exit("", "", 0)))
	r.Script("block", memexecutor.OutcomeUnknown(memexecutor.Block("ready")))
	client := commandClient(t, r)
	unknown := executorv1.CommandState_COMMAND_STATE_OUTCOME_UNKNOWN

	res := recvResult(t, startCommand(t.Context(), t, client, "exit"))
	if res.GetState() != unknown {
		t.Fatalf("exit result = %v, want outcome unknown", res)
	}
	stream := startCommand(t.Context(), t, client, "block")
	recvStdout(t, stream)
	sendCancel(t, stream)
	if res = recvResult(t, stream); res.GetState() != unknown {
		t.Fatalf("cancelled result = %v, want outcome unknown", res)
	}
}

func TestRefusedStartNeverRuns(t *testing.T) {
	r := memexecutor.NewRunner()
	r.Refuse("sh", tool.ErrNoShell)
	client := commandClient(t, r)
	for command, want := range map[string]codes.Code{
		"sh":      codes.FailedPrecondition,
		"missing": codes.Internal,
	} {
		stream, err := client.RunCommand(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&executorv1.RunCommandRequest{
			Action: &executorv1.RunCommandRequest_Start{
				Start: &executorv1.StartCommand{Command: command},
			},
		}); err != nil {
			t.Fatal(err)
		}
		if msg, err := stream.Recv(); status.Code(err) != want {
			t.Fatalf("%s: %v, %v; want %v", command, msg, err, want)
		}
	}
}

func TestUnregisteredServiceIsUnimplemented(t *testing.T) {
	conn := memexecutor.Dial(t, memexecutor.Executor{Commands: memexecutor.NewRunner()})
	_, err := executorv1.NewFileServiceClient(conn).StatFile(t.Context(),
		&executorv1.StatFileRequest{Path: "x"})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("StatFile = %v, want Unimplemented", err)
	}
}
