package executorclient_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
	"github.com/stacklok/mecatl/internal/executorrpc/executorclient"
	"github.com/stacklok/mecatl/internal/executorrpc/memexecutor"
)

func TestRunReportsEachOutcome(t *testing.T) {
	r := memexecutor.NewRunner()
	r.Script("succeed", memexecutor.Exit("out", "err", 0))
	r.Script("fail", memexecutor.Exit("", "bad\xff", 3))
	r.Script("survivor", memexecutor.OutcomeUnknown(memexecutor.Exit("", "", 0)))
	r.Script("block", memexecutor.Block("ready"))
	r.Refuse("noshell", tool.ErrNoShell)
	r.Refuse("broken", errors.New("exec failed"))
	runner := executorclient.NewRunner(serve(t, nil, r).conn, root, nil)
	if runner.BoundWorkspaceRoot() != root {
		t.Fatalf("root = %q", runner.BoundWorkspaceRoot())
	}

	res, err := runner.Run(t.Context(), "succeed")
	if err != nil || res != (tool.CommandResult{Stdout: "out", Stderr: "err"}) {
		t.Fatalf("succeed = %+v, %v", res, err)
	}
	res, err = runner.Run(t.Context(), "fail")
	if err != nil || res.ExitCode != 3 || res.Stderr != "bad�" {
		t.Fatalf("fail = %+v, %v", res, err)
	}
	_, err = runner.Run(t.Context(), "survivor")
	if !errors.Is(err, executorclient.ErrOutcomeUnknown) {
		t.Fatalf("survivor = %v, want outcome unknown", err)
	}
	if _, err := runner.Run(t.Context(), "noshell"); !errors.Is(err, tool.ErrNoShell) {
		t.Fatalf("noshell = %v, want ErrNoShell", err)
	}
	_, err = runner.Run(t.Context(), "broken")
	if err == nil || errors.Is(err, executorclient.ErrOutcomeUnknown) {
		t.Fatalf("refused start = %v, want a known failure", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	res, err = runner.Run(ctx, "block")
	if !errors.Is(err, context.DeadlineExceeded) || res.Stdout != "ready" {
		t.Fatalf("deadline = %+v, %v", res, err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	res, err = runner.Run(ctx, "block")
	if !errors.Is(err, context.Canceled) || res.Stdout != "ready" {
		t.Fatalf("cancel = %+v, %v", res, err)
	}
	if r.Running() != 0 {
		t.Fatalf("%d scripts still running", r.Running())
	}
}

type observedStream struct {
	grpc.ClientStream
	output chan<- struct{}
}

func (s *observedStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	if msg, ok := m.(*executorv1.RunCommandResponse); ok && err == nil && msg.GetStdout() != nil {
		select {
		case s.output <- struct{}{}:
		default:
		}
	}
	return err
}

func TestDroppedConnectionDuringCommandIsUnknownAndNotRetried(t *testing.T) {
	var starts, streams atomic.Int32
	r := memexecutor.NewRunner()
	r.Script("long", func(ctx context.Context, stdout, _ io.Writer) commandserver.Exit {
		starts.Add(1)
		_, _ = io.WriteString(stdout, "ready")
		<-ctx.Done()
		return commandserver.Exit{Code: -1, Confirmed: true}
	})
	output := make(chan struct{}, 1)
	observe := func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		streams.Add(1)
		cs, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			return nil, err
		}
		return &observedStream{ClientStream: cs, output: output}, nil
	}
	f := serve(t, nil, r, grpc.WithStreamInterceptor(observe))
	go func() {
		<-output
		f.dropConnections()
	}()
	res, err := executorclient.NewRunner(f.conn, root, nil).Run(t.Context(), "long")
	if !errors.Is(err, executorclient.ErrOutcomeUnknown) || res.Stdout != "ready" {
		t.Fatalf("dropped = %+v, %v; want outcome unknown", res, err)
	}
	if starts.Load() != 1 || streams.Load() != 1 {
		t.Fatalf("command started %d times over %d streams, want once",
			starts.Load(), streams.Load())
	}
}
