package executorclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"google.golang.org/grpc"
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
}

var _ tool.CommandRunner = (*Runner)(nil)

// NewRunner returns a Runner whose commands run in root, the server's workspace root.
func NewRunner(conn grpc.ClientConnInterface, root string, rc RequestContextFunc) *Runner {
	return &Runner{commands: executorv1.NewCommandServiceClient(conn), root: root, context: rc}
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
	return receive(ctx, stream)
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

// receive reads events until the result. A failure before the start
// confirmation means the command never ran; any later failure leaves it unknown.
func receive(ctx context.Context, stream commandStream) (tool.CommandResult, error) {
	var stdout, stderr bytes.Buffer
	output := func() tool.CommandResult {
		return tool.CommandResult{
			Stdout: session.ToValidUTF8(stdout.String()),
			Stderr: session.ToValidUTF8(stderr.String()),
		}
	}
	started := false
	for {
		msg, err := stream.Recv()
		if err != nil && !started {
			return tool.CommandResult{}, notStarted(err)
		}
		if err != nil {
			return output(), fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
		}
		started = true
		switch ev := msg.GetEvent().(type) {
		case *executorv1.RunCommandResponse_Stdout:
			stdout.Write(ev.Stdout)
		case *executorv1.RunCommandResponse_Stderr:
			stderr.Write(ev.Stderr)
		case *executorv1.RunCommandResponse_Result:
			out := output()
			out.ExitCode = int(ev.Result.GetExitCode())
			return out, resultError(ctx, ev.Result.GetState())
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

func notStarted(err error) error {
	if reason(err) == executorv1.ErrorReason_ERROR_REASON_NO_SHELL {
		return tool.ErrNoShell
	}
	return fmt.Errorf("executor: command did not start: %w", err)
}
