package commandserver_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/executorrpc/commandconformance"
	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
)

type fakeRunner struct{ children atomic.Int32 }

type fakeProcess struct{ wait func() commandserver.Exit }

func (p fakeProcess) Wait() commandserver.Exit { return p.wait() }

func (r *fakeRunner) Start(
	ctx context.Context, command string, stdout, stderr io.Writer,
) (commandserver.Process, error) {
	block := func(child bool) commandserver.Exit {
		if child {
			r.children.Add(1)
			defer r.children.Add(-1)
		}
		_, _ = io.WriteString(stdout, "ready")
		<-ctx.Done()
		return commandserver.Exit{Code: -1, Confirmed: true}
	}
	switch command {
	case "succeed":
		return fakeProcess{func() commandserver.Exit {
			_, _ = io.WriteString(stdout, "out")
			_, _ = io.WriteString(stderr, "err")
			return commandserver.Exit{Confirmed: true}
		}}, nil
	case "fail":
		return fakeProcess{func() commandserver.Exit {
			return commandserver.Exit{Code: 3, Confirmed: true}
		}}, nil
	case "block", "block-child":
		child := command == "block-child"
		return fakeProcess{func() commandserver.Exit { return block(child) }}, nil
	case "survivor":
		return fakeProcess{func() commandserver.Exit { return commandserver.Exit{} }}, nil
	case "big":
		return fakeProcess{func() commandserver.Exit {
			_, _ = io.WriteString(stdout, strings.Repeat("o", 6))
			_, _ = io.WriteString(stderr, strings.Repeat("e", 6))
			return commandserver.Exit{Confirmed: true}
		}}, nil
	case "noshell":
		return nil, tool.ErrNoShell
	case "early":
		_, _ = io.WriteString(stdout, "early")
		return fakeProcess{func() commandserver.Exit {
			return commandserver.Exit{Confirmed: true}
		}}, nil
	}
	return nil, errors.New("unknown command")
}

func newServer(
	t *testing.T, r commandserver.Runner, opts ...commandserver.Option,
) *commandserver.Server {
	t.Helper()
	srv, err := commandserver.New(r, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestNonPositiveLimitsAreRejected(t *testing.T) {
	for _, opt := range []commandserver.Option{
		commandserver.WithMaxTimeout(0),
		commandserver.WithMaxOutputBytes(0),
		commandserver.WithMaxCommandBytes(-1),
	} {
		if _, err := commandserver.New(&fakeRunner{}, opt); err == nil {
			t.Fatal("New accepted a non-positive limit")
		}
	}
}

func TestConformanceWithScriptedRunner(t *testing.T) {
	runner := &fakeRunner{}
	client := commandconformance.Dial(t, newServer(t, runner))
	commandconformance.Run(t, commandconformance.Harness{
		Client:         func(*testing.T) executorv1.CommandServiceClient { return client },
		Succeed:        "succeed",
		Fail:           "fail",
		Block:          "block",
		BlockWithChild: "block-child",
		ChildGone: func(ctx context.Context) error {
			for runner.children.Load() != 0 {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				time.Sleep(time.Millisecond)
			}
			return nil
		},
		Survivor: "survivor",
	})
}

func run(t *testing.T, client executorv1.CommandServiceClient, command string) (
	string, *executorv1.CommandResult, error,
) {
	t.Helper()
	stream, err := client.RunCommand(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&executorv1.RunCommandRequest{Action: &executorv1.RunCommandRequest_Start{
		Start: &executorv1.StartCommand{Command: command},
	}}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for {
		msg, err := stream.Recv()
		if err != nil {
			return out.String(), nil, err
		}
		out.Write(msg.GetStdout())
		out.Write(msg.GetStderr())
		if msg.GetResult() != nil {
			return out.String(), msg.GetResult(), nil
		}
	}
}

func TestOutputIsBoundedAndFlagged(t *testing.T) {
	srv := newServer(t, &fakeRunner{}, commandserver.WithMaxOutputBytes(8))
	client := commandconformance.Dial(t, srv)
	out, res, err := run(t, client, "big")
	if err != nil || out != "ooooooee" || !res.GetOutputTruncated() {
		t.Fatalf("out %q result %v err %v", out, res, err)
	}
}

func TestStartFailures(t *testing.T) {
	srv := newServer(t, &fakeRunner{}, commandserver.WithMaxCommandBytes(8))
	client := commandconformance.Dial(t, srv)
	for command, want := range map[string]codes.Code{
		"noshell":        codes.FailedPrecondition,
		"unknown":        codes.Internal,
		"far too long !": codes.ResourceExhausted,
	} {
		_, _, err := run(t, client, command)
		if status.Code(err) != want {
			t.Fatalf("%s: %v, want %v", command, err, want)
		}
	}
}

func TestSecondStartIsRejected(t *testing.T) {
	client := commandconformance.Dial(t, newServer(t, &fakeRunner{}))
	stream, err := client.RunCommand(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	start := &executorv1.RunCommandRequest{Action: &executorv1.RunCommandRequest_Start{
		Start: &executorv1.StartCommand{Command: "block"},
	}}
	for range 2 {
		if err := stream.Send(start); err != nil {
			t.Fatal(err)
		}
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("got %v, want InvalidArgument", err)
			}
			return
		}
		if msg.GetResult() != nil {
			t.Fatalf("unexpected result %v", msg)
		}
	}
}

func TestOutputBeforeStartedIsHeld(t *testing.T) {
	stream, err := commandconformance.Dial(t, newServer(t, &fakeRunner{})).RunCommand(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&executorv1.RunCommandRequest{Action: &executorv1.RunCommandRequest_Start{
		Start: &executorv1.StartCommand{Command: "early"},
	}}); err != nil {
		t.Fatal(err)
	}
	var events []string
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case msg.GetStarted() != nil:
			events = append(events, "started")
		case msg.GetStdout() != nil:
			events = append(events, string(msg.GetStdout()))
		case msg.GetResult() != nil:
			if got := strings.Join(events, ","); got != "started,early" {
				t.Fatalf("events = %s, want started,early", got)
			}
			return
		}
	}
}
