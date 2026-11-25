package executioncontroller

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/executionenv"
)

// commandCancelGrace bounds the wait for a cancelled command's outcome.
var commandCancelGrace = 30 * time.Second

const fenceTimeout = 10 * time.Second

var (
	errNoCommandResult = status.Error(
		codes.Unknown,
		"executor ended the command without a result",
	)
	errEmptyCommandResult = status.Error(codes.Unknown, "executor sent an empty command result")
)

// CommandProxy is the provider's authorizing CommandService.
type CommandProxy struct {
	executorv1.UnimplementedCommandServiceServer
	proxy executorProxy
}

// NewCommandProxy authorizes with h's client policy and backend.
func NewCommandProxy(h *Handler, connector ExecutorConnectFunc) *CommandProxy {
	return &CommandProxy{proxy: executorProxy{
		handler:    h,
		connector:  connector,
		capability: executionenv.CapabilityForegroundCommand,
	}}
}

type commandStream = grpc.BidiStreamingServer[
	executorv1.RunCommandRequest,
	executorv1.RunCommandResponse,
]

// RunCommand authorizes the first message, caps the command's timeout at its template's,
// and relays the executor's events, clipping output at MaxCommandBytes. A command that
// started and ends without a known outcome fences its environment.
func (p *CommandProxy) RunCommand(stream commandStream) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	ctx, f := p.proxy.track(stream.Context(), first.GetContext())
	route, err := p.proxy.authorize(ctx, first.GetContext())
	if err != nil {
		f.release()
		return notForwarded(withdrawnOr(ctx, err))
	}
	forward, err := boundedStart(first, route)
	if err != nil {
		f.release()
		return notForwarded(err)
	}
	a, err := p.proxy.connect(ctx, route, f)
	if err != nil {
		return notForwarded(err)
	}
	defer a.release()
	upCtx, closeUp := context.WithCancel(context.WithoutCancel(ctx))
	defer closeUp()
	up, err := executorv1.NewCommandServiceClient(a.conn).RunCommand(upCtx)
	if err != nil {
		return notForwarded(withdrawnOr(ctx, executorError(err)))
	}
	if err := up.Send(forward); err != nil {
		if _, err = up.Recv(); err == nil {
			err = io.ErrUnexpectedEOF
		}
		return withdrawnOr(ctx, executorError(err))
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	u := &upstreamCommand{stream: up}
	done := make(chan struct{})
	defer close(done)
	go relayCommandControl(stream, u, stop)
	go cancelOnDone(ctx, u, closeUp, done)
	result, started, err := relayCommandOutput(ctx, stream, up, route.MaxCommandBytes, stop)
	if started && !knownOutcome(result) {
		if fenceErr := p.fence(ctx, first.GetContext(), route.Pin); fenceErr != nil {
			return fenceErr
		}
	}
	if err == nil && ctx.Err() == nil {
		err = stream.Send(&executorv1.RunCommandResponse{
			Event: &executorv1.RunCommandResponse_Result{Result: result},
		})
	} else if err == nil {
		err = status.FromContextError(ctx.Err()).Err()
	}
	return withdrawnOr(ctx, err)
}

type commandFencer interface {
	FenceOutcomeUnknown(ctx context.Context, env string, pin ExecutorPin) error
}

func (p *CommandProxy) fence(
	ctx context.Context,
	rc *executorv1.RequestContext,
	pin ExecutorPin,
) error {
	err := errors.New("command outcome unknown and the environment cannot be fenced")
	if fencer, ok := p.proxy.handler.backend.(commandFencer); ok {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fenceTimeout)
		defer cancel()
		err = fencer.FenceOutcomeUnknown(ctx, rc.GetEnvironment().GetId(), pin)
	}
	if err == nil {
		return nil
	}
	if d := p.proxy.handler.cfg.Diagnostics; d != nil {
		d.Log(
			ctx,
			port.LevelWarn,
			"command outcome unknown; fencing failed",
			"reason",
			backendReasonClass(err),
		)
	}
	return status.Error(codes.Unknown, "command outcome unknown")
}

// knownOutcome reports a result that proves the command left no process running.
func knownOutcome(result *executorv1.CommandResult) bool {
	switch result.GetState() {
	case executorv1.CommandState_COMMAND_STATE_SUCCEEDED,
		executorv1.CommandState_COMMAND_STATE_FAILED,
		executorv1.CommandState_COMMAND_STATE_CANCELLED,
		executorv1.CommandState_COMMAND_STATE_TIMED_OUT:
		return true
	}
	return false
}

func boundedStart(
	first *executorv1.RunCommandRequest,
	route ExecutorRoute,
) (*executorv1.RunCommandRequest, error) {
	start := first.GetStart()
	if start.GetCommand() == "" {
		return nil, wireError(executionenv.CodeInvalidArgument, false)
	}
	if int64(len(start.GetCommand())) > route.MaxCommandBytes {
		return nil, wireError(executionenv.CodeResourceExhausted, false)
	}
	timeout := route.MaxCommandDuration
	if d := start.GetTimeout(); d != nil {
		if d.CheckValid() != nil || d.AsDuration() < 0 {
			return nil, wireError(executionenv.CodeInvalidArgument, false)
		}
		if d.AsDuration() > 0 {
			timeout = min(d.AsDuration(), timeout)
		}
	}
	forward := proto.CloneOf(first)
	forward.GetStart().Timeout = durationpb.New(timeout)
	return forward, nil
}

// upstreamCommand serializes sends on the executor stream.
type upstreamCommand struct {
	mu     sync.Mutex
	stream grpc.BidiStreamingClient[executorv1.RunCommandRequest, executorv1.RunCommandResponse]
}

func (u *upstreamCommand) send(msg *executorv1.RunCommandRequest) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.stream.Send(msg)
}

func (u *upstreamCommand) closeSend() {
	u.mu.Lock()
	defer u.mu.Unlock()
	_ = u.stream.CloseSend()
}

// relayCommandControl forwards the caller's messages; the caller closing its
// side half-closes the executor stream, and a broken caller stream stops the call.
func relayCommandControl(stream commandStream, u *upstreamCommand, stop context.CancelFunc) {
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			u.closeSend()
			return
		}
		if err != nil {
			stop()
			return
		}
		if u.send(msg) != nil {
			return
		}
	}
}

// cancelOnDone asks the executor to cancel once ctx ends, so the outcome is
// still reported, and closes the stream if none arrives within commandCancelGrace.
func cancelOnDone(
	ctx context.Context,
	u *upstreamCommand,
	closeUp context.CancelFunc,
	done <-chan struct{},
) {
	select {
	case <-done:
		return
	case <-ctx.Done():
	}
	err := u.send(&executorv1.RunCommandRequest{
		Action: &executorv1.RunCommandRequest_Cancel{Cancel: &executorv1.CancelCommand{}},
	})
	if err != nil {
		closeUp()
		return
	}
	grace := time.NewTimer(commandCancelGrace)
	defer grace.Stop()
	select {
	case <-done:
	case <-grace.C:
		closeUp()
	}
}

// relayCommandOutput relays events until the result, which it returns unsent.
// Once ctx ends it stops relaying but still waits for the outcome. started
// reports that the executor sent any event.
func relayCommandOutput(
	ctx context.Context,
	stream commandStream,
	up grpc.BidiStreamingClient[executorv1.RunCommandRequest, executorv1.RunCommandResponse],
	limit int64,
	stop context.CancelFunc,
) (*executorv1.CommandResult, bool, error) {
	var sent int64
	clipped, started := false, false
	for {
		msg, err := up.Recv()
		if errors.Is(err, io.EOF) {
			return nil, started, errNoCommandResult
		}
		if err != nil {
			return nil, started, executorError(err)
		}
		started = true
		switch event := msg.GetEvent().(type) {
		case *executorv1.RunCommandResponse_Stdout:
			event.Stdout, clipped = clip(event.Stdout, limit-sent, clipped)
			sent += int64(len(event.Stdout))
			if len(event.Stdout) == 0 {
				continue
			}
		case *executorv1.RunCommandResponse_Stderr:
			event.Stderr, clipped = clip(event.Stderr, limit-sent, clipped)
			sent += int64(len(event.Stderr))
			if len(event.Stderr) == 0 {
				continue
			}
		case *executorv1.RunCommandResponse_Result:
			if event.Result == nil {
				return nil, started, errEmptyCommandResult
			}
			event.Result.OutputTruncated = event.Result.GetOutputTruncated() || clipped
			return event.Result, started, nil
		}
		if ctx.Err() == nil && stream.Send(msg) != nil {
			stop()
		}
	}
}

func clip(data []byte, remaining int64, clipped bool) ([]byte, bool) {
	if int64(len(data)) <= remaining {
		return data, clipped
	}
	return data[:max(remaining, 0)], true
}
