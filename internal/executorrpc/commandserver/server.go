// Package commandserver serves the executor CommandService over a Runner.
package commandserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/tool"
)

// Runner starts commands for the service.
type Runner interface {
	// Start returns once the command runs, or an error if it never ran.
	// Ending ctx kills the command and every process it started.
	Start(ctx context.Context, command string, stdout, stderr io.Writer) (Process, error)
}

// Process is a started command.
type Process interface {
	// Wait returns after the command ends and its output has been written.
	Wait() Exit
}

// Exit is how a command ended.
type Exit struct {
	Code int
	// Confirmed reports that no process the command started is left running.
	Confirmed bool
}

const (
	defaultMaxTimeout      = 30 * time.Minute
	defaultMaxOutputBytes  = 5 << 20
	defaultMaxCommandBytes = 1 << 20
	maxChunkBytes          = 32 << 10
)

var (
	errCancelRequested = errors.New("command cancel requested")
	errTimedOut        = errors.New("command timed out")
	errProtocol        = errors.New("unexpected message after start")
)

// Server implements executorv1.CommandServiceServer.
type Server struct {
	executorv1.UnimplementedCommandServiceServer
	runner          Runner
	maxTimeout      time.Duration
	maxOutputBytes  int
	maxCommandBytes int
}

// Option configures a Server.
type Option func(*Server) error

// WithMaxTimeout caps the timeout of every command.
func WithMaxTimeout(d time.Duration) Option {
	return func(s *Server) error {
		if d <= 0 {
			return errors.New("max timeout must be positive")
		}
		s.maxTimeout = d
		return nil
	}
}

// WithMaxOutputBytes bounds the combined stdout and stderr of a command.
func WithMaxOutputBytes(n int) Option {
	return func(s *Server) error {
		if n <= 0 {
			return errors.New("max output bytes must be positive")
		}
		s.maxOutputBytes = n
		return nil
	}
}

// WithMaxCommandBytes bounds the length of a command.
func WithMaxCommandBytes(n int) Option {
	return func(s *Server) error {
		if n <= 0 {
			return errors.New("max command bytes must be positive")
		}
		s.maxCommandBytes = n
		return nil
	}
}

// New returns a server that runs commands with runner.
func New(runner Runner, opts ...Option) (*Server, error) {
	s := &Server{
		runner:          runner,
		maxTimeout:      defaultMaxTimeout,
		maxOutputBytes:  defaultMaxOutputBytes,
		maxCommandBytes: defaultMaxCommandBytes,
	}
	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// RunCommand runs one command per stream.
func (s *Server) RunCommand(
	stream grpc.BidiStreamingServer[executorv1.RunCommandRequest, executorv1.RunCommandResponse],
) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	timeout, err := s.validate(first.GetStart())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancelCause(stream.Context())
	defer cancel(nil)
	runCtx, cancelTimeout := context.WithTimeoutCause(ctx, timeout, errTimedOut)
	defer cancelTimeout()

	out := &output{stream: stream, remaining: s.maxOutputBytes}
	proc, err := s.runner.Start(
		runCtx,
		first.GetStart().GetCommand(),
		streamWriter{out, stdoutEvent},
		streamWriter{out, stderrEvent},
	)
	if err != nil {
		if errors.Is(err, tool.ErrNoShell) {
			return failure(codes.FailedPrecondition, executorv1.ErrorReason_ERROR_REASON_NO_SHELL,
				"no shell available")
		}
		return failure(codes.Internal, executorv1.ErrorReason_ERROR_REASON_START_FAILED,
			"command did not start")
	}
	if err := out.start(); err != nil {
		cancel(err)
		proc.Wait()
		return err
	}
	go receiveCancel(stream, cancel)

	exit := proc.Wait()
	cause := context.Cause(runCtx)
	if errors.Is(cause, errProtocol) {
		return status.Error(codes.InvalidArgument, errProtocol.Error())
	}
	return out.send(&executorv1.RunCommandResponse{Event: &executorv1.RunCommandResponse_Result{
		Result: &executorv1.CommandResult{
			State:           classify(exit, cause),
			ExitCode:        int32(exit.Code), //nolint:gosec // Exit statuses fit in int32.
			OutputTruncated: out.truncated(),
		},
	}})
}

func (s *Server) validate(start *executorv1.StartCommand) (time.Duration, error) {
	if start == nil {
		return 0, status.Error(codes.InvalidArgument, "first message must start a command")
	}
	if start.GetCommand() == "" {
		return 0, status.Error(codes.InvalidArgument, "command is required")
	}
	if len(start.GetCommand()) > s.maxCommandBytes {
		return 0, failure(codes.ResourceExhausted,
			executorv1.ErrorReason_ERROR_REASON_LIMIT_EXCEEDED, "command exceeds limit")
	}
	timeout := s.maxTimeout
	if d := start.GetTimeout(); d != nil {
		if err := d.CheckValid(); err != nil || d.AsDuration() < 0 {
			return 0, status.Error(codes.InvalidArgument, "invalid timeout")
		}
		if requested := d.AsDuration(); requested > 0 && requested < timeout {
			timeout = requested
		}
	}
	return timeout, nil
}

func receiveCancel(
	stream grpc.BidiStreamingServer[executorv1.RunCommandRequest, executorv1.RunCommandResponse],
	cancel context.CancelCauseFunc,
) {
	for {
		msg, err := stream.Recv()
		switch {
		case errors.Is(err, io.EOF):
			return
		case err != nil:
			cancel(err)
			return
		case msg.GetCancel() != nil:
			cancel(errCancelRequested)
		default:
			cancel(errProtocol)
			return
		}
	}
}

func classify(exit Exit, cause error) executorv1.CommandState {
	switch {
	case !exit.Confirmed:
		return executorv1.CommandState_COMMAND_STATE_OUTCOME_UNKNOWN
	case errors.Is(cause, errTimedOut):
		return executorv1.CommandState_COMMAND_STATE_TIMED_OUT
	case cause != nil:
		return executorv1.CommandState_COMMAND_STATE_CANCELLED
	case exit.Code == 0:
		return executorv1.CommandState_COMMAND_STATE_SUCCEEDED
	default:
		return executorv1.CommandState_COMMAND_STATE_FAILED
	}
}

func failure(code codes.Code, reason executorv1.ErrorReason, msg string) error {
	st, err := status.New(code, msg).WithDetails(&executorv1.ErrorDetail{Reason: reason})
	if err != nil {
		return status.Error(code, msg)
	}
	return st.Err()
}

type event int

const (
	stdoutEvent event = iota
	stderrEvent
)

// output serializes sends and bounds the combined stdout and stderr bytes.
// Output written before started is held so started is always the first event.
type output struct {
	mu        sync.Mutex
	stream    grpc.BidiStreamingServer[executorv1.RunCommandRequest, executorv1.RunCommandResponse]
	remaining int
	clipped   bool
	started   bool
	pending   []*executorv1.RunCommandResponse
}

func (o *output) send(msg *executorv1.RunCommandResponse) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stream.Send(msg)
}

func (o *output) start() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.stream.Send(&executorv1.RunCommandResponse{
		Event: &executorv1.RunCommandResponse_Started{Started: &executorv1.CommandStarted{}},
	}); err != nil {
		return err
	}
	o.started = true
	for _, msg := range o.pending {
		if err := o.stream.Send(msg); err != nil {
			return err
		}
	}
	o.pending = nil
	return nil
}

func (o *output) truncated() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.clipped
}

// streamWriter sends one output stream's bytes as events of its kind.
type streamWriter struct {
	o    *output
	kind event
}

func (w streamWriter) Write(p []byte) (int, error) {
	o := w.o
	o.mu.Lock()
	defer o.mu.Unlock()
	data := p
	if len(data) > o.remaining {
		data = data[:o.remaining]
		o.clipped = true
	}
	o.remaining -= len(data)
	for len(data) > 0 {
		chunk := data[:min(len(data), maxChunkBytes)]
		data = data[len(chunk):]
		msg := &executorv1.RunCommandResponse{}
		if !o.started {
			chunk = bytes.Clone(chunk)
		}
		if w.kind == stderrEvent {
			msg.Event = &executorv1.RunCommandResponse_Stderr{Stderr: chunk}
		} else {
			msg.Event = &executorv1.RunCommandResponse_Stdout{Stdout: chunk}
		}
		if !o.started {
			o.pending = append(o.pending, msg)
			continue
		}
		if err := o.stream.Send(msg); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
