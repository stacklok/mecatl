package guestexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
)

// Handler returns the exec service for the shared authenticated guest multiplexer.
func (s *GuestServer) Handler() control.Handler {
	return func(ctx context.Context, method string, payload json.RawMessage, send func(any) error) (any, string, error) {
		if method != "run" {
			return nil, "unsupported_method", control.ErrMalformedFrame
		}
		var request requestFrame
		if err := json.Unmarshal(payload, &request); err != nil {
			return nil, "malformed", control.ErrMalformedFrame
		}
		if request.TemporaryScope != "" && request.TemporaryScope != tool.TemporaryScopeManaged && request.TemporaryScope != tool.TemporaryScopeSystem {
			return nil, "malformed", control.ErrMalformedFrame
		}
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		default:
			return nil, codeConcurrency, ErrConcurrencyLimit
		}
		if s.onStart != nil {
			s.onStart(request.Command)
		}
		return s.execute(ctx, send, request.Command, request.TemporaryScope)
	}
}

func guestCommandEnv(contract RuntimeContract, gitDir, worktreeRoot string) []string {
	return append(contract.commandEnvironment(),
		"GIT_DIR="+gitDir,
		"GIT_WORK_TREE="+worktreeRoot,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"PAGER=cat",
	)
}

func (s *GuestServer) execute(ctx context.Context, send func(any) error, command string, scope tool.TemporaryScope) (any, string, error) { //nolint:gocyclo // explicit process/output/cancel state machine
	commandEnv := guestCommandEnv(s.runtime, s.gitDir, s.root)
	if scope == tool.TemporaryScopeManaged {
		tempDir, err := os.MkdirTemp("/tmp", "mecatl-managed-")
		if err != nil {
			return nil, codeExecution, err
		}
		defer func() { _ = os.RemoveAll(tempDir) }()
		if os.Geteuid() != int(s.identity.UID) || os.Getegid() != int(s.identity.GID) {
			if err := os.Chown(tempDir, int(s.identity.UID), int(s.identity.GID)); err != nil {
				return nil, codeExecution, err
			}
		}
		commandEnv = append(commandEnv, "TMPDIR="+tempDir, "GOTMPDIR="+tempDir)
	}
	processCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	cmd := exec.CommandContext(processCtx, s.shell, "-c", command) // #nosec G204 -- command is the explicit guest exec payload.
	cmd.Dir = s.root
	cmd.Env = commandEnv
	configureProcessGroup(cmd, s.limits.CancelGrace)
	configureWorkloadIdentity(cmd, s.identity)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, codeExecution, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, codeExecution, err
	}
	if err := cmd.Start(); err != nil {
		return nil, codeExecution, err
	}

	type output struct {
		channel string
		data    []byte
		err     error
	}
	outputs := make(chan output, 4)
	var readers sync.WaitGroup
	readers.Add(2)
	readPipe := func(channel string, src io.Reader) {
		defer readers.Done()
		bufferSize := min(int(s.limits.MaxFrameBytes/2), 32<<10)
		buffer := make([]byte, bufferSize)
		for {
			n, readErr := src.Read(buffer)
			if n > 0 {
				chunk := append([]byte(nil), buffer[:n]...)
				select {
				case outputs <- output{channel: channel, data: chunk}:
				case <-processCtx.Done():
					return
				}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					select {
					case outputs <- output{err: readErr}:
					case <-processCtx.Done():
					}
				}
				return
			}
		}
	}
	go readPipe(channelStdout, stdout)
	go readPipe(channelStderr, stderr)
	waitDone := make(chan error, 1)
	go func() {
		readers.Wait()
		close(outputs)
		waitDone <- cmd.Wait()
	}()
	var outputBytes int64
	var waitErr error
	waited := false
	for outputs != nil || !waited {
		select {
		case item, ok := <-outputs:
			if !ok {
				outputs = nil
				continue
			}
			if item.err != nil {
				cancel(item.err)
				continue
			}
			outputBytes += int64(len(item.data))
			if outputBytes > s.limits.MaxOutputBytes {
				cancel(ErrOutputOverflow)
				return nil, codeOutputOverflow, ErrOutputOverflow
			}
			if err := send(responseFrame{Type: frameOutput, Channel: item.channel, Data: item.data}); err != nil {
				cancel(err)
				return nil, codeExecution, err
			}
		case waitErr = <-waitDone:
			waited = true
			waitDone = nil
		case <-processCtx.Done():
			// configureProcessGroup terminates the command tree; continue draining
			// already-produced output until Wait completes.
		}
	}

	cause := context.Cause(processCtx)
	if cause != nil {
		if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
			return responseFrame{Type: frameError, Code: codeCancelled, ExitCode: exitCode(waitErr)}, codeCancelled, cause
		}
		return nil, codeExecution, fmt.Errorf("guest command execution: %w", cause)
	}
	return responseFrame{Type: frameExit, ExitCode: exitCode(waitErr)}, "", nil
}

func exitCode(waitErr error) int {
	if waitErr == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
