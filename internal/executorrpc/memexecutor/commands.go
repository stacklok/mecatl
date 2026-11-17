package memexecutor

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
)

// Script plays a command until it returns.
type Script func(ctx context.Context, stdout, stderr io.Writer) commandserver.Exit

// ErrUnknownCommand reports a command with no script.
var ErrUnknownCommand = errors.New("memexecutor: no script for command")

// Runner is a scripted commandserver.Runner.
type Runner struct {
	mu      sync.Mutex
	scripts map[string]Script
	refusal map[string]error
	running atomic.Int32
}

// NewRunner returns an empty Runner.
func NewRunner() *Runner {
	return &Runner{scripts: map[string]Script{}, refusal: map[string]error{}}
}

// Script plays s for command.
func (r *Runner) Script(command string, s Script) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scripts[command] = s
	delete(r.refusal, command)
}

// Refuse fails every start of command with err.
func (r *Runner) Refuse(command string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refusal[command] = err
	delete(r.scripts, command)
}

// Running counts unfinished scripts.
func (r *Runner) Running() int { return int(r.running.Load()) }

// Start implements commandserver.Runner.
func (r *Runner) Start(
	ctx context.Context,
	command string,
	stdout io.Writer,
	stderr io.Writer,
) (commandserver.Process, error) {
	r.mu.Lock()
	script, ok := r.scripts[command]
	refusal := r.refusal[command]
	r.mu.Unlock()
	if refusal != nil {
		return nil, refusal
	}
	if !ok {
		return nil, ErrUnknownCommand
	}
	p := &process{done: make(chan struct{})}
	r.running.Add(1)
	go func() {
		defer close(p.done)
		defer r.running.Add(-1)
		p.exit = script(ctx, stdout, stderr)
	}()
	return p, nil
}

type process struct {
	done chan struct{}
	exit commandserver.Exit
}

func (p *process) Wait() commandserver.Exit {
	<-p.done
	return p.exit
}

// Exit writes stdout and stderr, then exits with code.
func Exit(stdout, stderr string, code int) Script {
	return func(_ context.Context, out, errOut io.Writer) commandserver.Exit {
		write(out, stdout)
		write(errOut, stderr)
		return commandserver.Exit{Code: code, Confirmed: true}
	}
}

// Stream writes one chunk to stdout per receive from next, then exits 0.
func Stream(next <-chan struct{}, chunks ...string) Script {
	return func(ctx context.Context, out, _ io.Writer) commandserver.Exit {
		for _, chunk := range chunks {
			select {
			case <-next:
			case <-ctx.Done():
				return killed()
			}
			write(out, chunk)
		}
		return commandserver.Exit{Confirmed: true}
	}
}

// Block writes ready to stdout, then waits for ctx.
func Block(ready string) Script {
	return func(ctx context.Context, out, _ io.Writer) commandserver.Exit {
		write(out, ready)
		<-ctx.Done()
		return killed()
	}
}

// OutcomeUnknown plays s without confirming cleanup.
func OutcomeUnknown(s Script) Script {
	return func(ctx context.Context, out, errOut io.Writer) commandserver.Exit {
		exit := s(ctx, out, errOut)
		exit.Confirmed = false
		return exit
	}
}

func killed() commandserver.Exit { return commandserver.Exit{Code: -1, Confirmed: true} }

func write(w io.Writer, s string) {
	if s != "" {
		_, _ = io.WriteString(w, s)
	}
}
