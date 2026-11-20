package executorclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// ErrOutcomeUnknown reports a command that started but whose end, or the end
// of every process it started, was not confirmed. Such a command is never retried.
var ErrOutcomeUnknown = errors.New("executor: command outcome unknown")

// cancelGrace bounds the wait for a result after asking the server to cancel.
var cancelGrace = 30 * time.Second

// Runner is a foreground tool.CommandRunner served by a CommandService. It
// starts each command once; conn must not enable gRPC retries.
type Runner struct {
	commands executorv1.CommandServiceClient
	root     string
	context  RequestContextFunc
	limits   limits
}

var _ tool.CommandRunner = (*Runner)(nil)

// NewRunner returns a Runner whose commands run in root, the server's workspace root.
func NewRunner(
	conn grpc.ClientConnInterface,
	root string,
	rc RequestContextFunc,
	opts ...Option,
) *Runner {
	return &Runner{
		commands: executorv1.NewCommandServiceClient(conn),
		root:     root,
		context:  rc,
		limits:   newLimits(opts),
	}
}

// BoundWorkspaceRoot returns the root commands run in.
func (r *Runner) BoundWorkspaceRoot() string { return r.root }

type commandStream = grpc.BidiStreamingClient[
	executorv1.RunCommandRequest,
	executorv1.RunCommandResponse,
]

// Run implements tool.CommandRunner. Ending ctx cancels the command and still
// waits, up to cancelGrace, for its result.
func (r *Runner) Run(ctx context.Context, command string) (tool.CommandResult, error) {
	rc, err := r.context.get(ctx)
	if err != nil {
		return tool.CommandResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return tool.CommandResult{}, err
	}
	streamCtx, closeStream := context.WithCancel(context.WithoutCancel(ctx))
	defer closeStream()
	stream, err := r.commands.RunCommand(streamCtx)
	if err != nil {
		return tool.CommandResult{}, notStarted(err)
	}
	start := &executorv1.StartCommand{Command: command}
	if deadline, ok := ctx.Deadline(); ok {
		start.Timeout = durationpb.New(max(time.Until(deadline), time.Millisecond))
	}
	err = stream.Send(&executorv1.RunCommandRequest{
		Context: rc,
		Action:  &executorv1.RunCommandRequest_Start{Start: start},
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return tool.CommandResult{}, notStarted(err)
	}
	done := make(chan struct{})
	defer close(done)
	go cancelOnDone(ctx, stream, closeStream, done)
	return receive(ctx, stream, r.limits.maxOutputBytes)
}

// cancelOnDone asks the server to cancel once ctx ends, and closes the stream,
// losing the result, if none arrives within cancelGrace.
func cancelOnDone(
	ctx context.Context,
	stream commandStream,
	closeStream context.CancelFunc,
	done <-chan struct{},
) {
	select {
	case <-done:
		return
	case <-ctx.Done():
	}
	err := stream.Send(&executorv1.RunCommandRequest{
		Action: &executorv1.RunCommandRequest_Cancel{Cancel: &executorv1.CancelCommand{}},
	})
	if err != nil {
		closeStream()
		return
	}
	grace := time.NewTimer(cancelGrace)
	defer grace.Stop()
	select {
	case <-done:
	case <-grace.C:
		closeStream()
	}
}

const truncationMarker = "\n... [output truncated]"

// receive reads events until the result. Only a refusal the server reports
// before Started means the command never ran; anything else leaves it unknown.
func receive(
	ctx context.Context,
	stream commandStream,
	maxOutput int,
) (tool.CommandResult, error) {
	var stdout, stderr bytes.Buffer
	truncated := false
	keep := func(buf *bytes.Buffer, p []byte) {
		n := min(len(p), maxOutput-stdout.Len()-stderr.Len())
		buf.Write(p[:n])
		truncated = truncated || n < len(p)
	}
	output := func() tool.CommandResult {
		out, errOut := stdout.String(), stderr.String()
		switch {
		case !truncated:
		case out == "" && errOut != "":
			errOut += truncationMarker
		default:
			out += truncationMarker
		}
		return tool.CommandResult{
			Stdout: session.ToValidUTF8(out),
			Stderr: session.ToValidUTF8(errOut),
		}
	}
	started := false
	for {
		msg, err := stream.Recv()
		if err != nil && !started {
			return tool.CommandResult{}, startError(err)
		}
		if err != nil {
			return output(), fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
		}
		switch ev := msg.GetEvent().(type) {
		case *executorv1.RunCommandResponse_Started:
			started = true
			continue
		case *executorv1.RunCommandResponse_Stdout:
			keep(&stdout, ev.Stdout)
		case *executorv1.RunCommandResponse_Stderr:
			keep(&stderr, ev.Stderr)
		case *executorv1.RunCommandResponse_Result:
			truncated = truncated || ev.Result.GetOutputTruncated()
			out := output()
			out.ExitCode = int(ev.Result.GetExitCode())
			if !started {
				return out, fmt.Errorf("%w: result before start", ErrOutcomeUnknown)
			}
			return out, resultError(ctx, ev.Result.GetState())
		}
		if !started {
			return output(), fmt.Errorf("%w: output before start", ErrOutcomeUnknown)
		}
	}
}

func resultError(ctx context.Context, state executorv1.CommandState) error {
	switch state {
	case executorv1.CommandState_COMMAND_STATE_SUCCEEDED,
		executorv1.CommandState_COMMAND_STATE_FAILED:
		return nil
	case executorv1.CommandState_COMMAND_STATE_CANCELLED:
		if err := ctx.Err(); err != nil {
			return err
		}
		return context.Canceled
	case executorv1.CommandState_COMMAND_STATE_TIMED_OUT:
		return fmt.Errorf("executor: command timed out: %w", context.DeadlineExceeded)
	case executorv1.CommandState_COMMAND_STATE_OUTCOME_UNKNOWN:
		return ErrOutcomeUnknown
	default:
		return fmt.Errorf("%w: state %v", ErrOutcomeUnknown, state)
	}
}

// startError classifies a failure before Started. Only a refusal the executor
// tagged means the command never ran; transport errors can follow a start.
func startError(err error) error {
	code, why := status.Code(err), reason(err)
	switch {
	case why == executorv1.ErrorReason_ERROR_REASON_NO_SHELL:
		return tool.ErrNoShell
	case why == executorv1.ErrorReason_ERROR_REASON_NOT_FORWARDED,
		code == codes.InvalidArgument,
		code == codes.ResourceExhausted &&
			why == executorv1.ErrorReason_ERROR_REASON_LIMIT_EXCEEDED,
		code == codes.Internal && why == executorv1.ErrorReason_ERROR_REASON_START_FAILED:
		return notStarted(err)
	}
	return fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
}

func notStarted(err error) error {
	return fmt.Errorf("executor: command did not start: %w", err)
}
