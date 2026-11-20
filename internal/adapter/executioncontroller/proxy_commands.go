package executioncontroller

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
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

// RunCommand authorizes the first message, bounds the command by its template,
// and relays the executor's events, clipping output at MaxCommandBytes.
func (p *CommandProxy) RunCommand(stream commandStream) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	ctx := stream.Context()
	route, err := p.proxy.authorize(ctx, first.GetContext())
	if err != nil {
		return notForwarded(err)
	}
	forward, err := boundedStart(first, route)
	if err != nil {
		return notForwarded(err)
	}
	a, err := p.proxy.connect(route)
	if err != nil {
		return notForwarded(err)
	}
	defer a.release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	up, err := executorv1.NewCommandServiceClient(a.conn).RunCommand(ctx)
	if err != nil {
		return notForwarded(executorError(err))
	}
	if err := up.Send(forward); err != nil {
		if _, err = up.Recv(); err == nil {
			err = io.ErrUnexpectedEOF
		}
		return executorError(err)
	}
	go relayCommandControl(stream, up, cancel)
	return relayCommandOutput(stream, up, route.MaxCommandBytes)
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
		if d.CheckValid() != nil || d.AsDuration() < 0 || d.AsDuration() > timeout {
			return nil, wireError(executionenv.CodeInvalidArgument, false)
		}
		if d.AsDuration() > 0 {
			timeout = d.AsDuration()
		}
	}
	forward := proto.CloneOf(first)
	forward.GetStart().Timeout = durationpb.New(timeout)
	return forward, nil
}

// relayCommandControl forwards cancels; the caller closing its side half-closes
// the executor stream, and a broken caller stream cancels it.
func relayCommandControl(
	stream commandStream,
	up grpc.BidiStreamingClient[executorv1.RunCommandRequest, executorv1.RunCommandResponse],
	cancel context.CancelFunc,
) {
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			_ = up.CloseSend()
			return
		}
		if err != nil || up.Send(msg) != nil {
			cancel()
			return
		}
	}
}

func relayCommandOutput(
	stream commandStream,
	up grpc.BidiStreamingClient[executorv1.RunCommandRequest, executorv1.RunCommandResponse],
	limit int64,
) error {
	var sent int64
	clipped := false
	for {
		msg, err := up.Recv()
		if errors.Is(err, io.EOF) {
			return status.Error(codes.Unknown, "executor ended the command without a result")
		}
		if err != nil {
			return executorError(err)
		}
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
				return status.Error(codes.Unknown, "executor sent an empty command result")
			}
			event.Result.OutputTruncated = event.Result.GetOutputTruncated() || clipped
			return stream.Send(msg)
		}
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
}

func clip(data []byte, remaining int64, clipped bool) ([]byte, bool) {
	if int64(len(data)) <= remaining {
		return data, clipped
	}
	return data[:max(remaining, 0)], true
}
