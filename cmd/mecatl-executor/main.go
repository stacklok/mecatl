// Command mecatl-executor runs the fixed credential-free workload helper.
//
// Without arguments it serves one Pod exec request. "serve" runs the executor
// gRPC server; "supervise-command" is that server's per-command helper.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/internal/executionenv"
	"github.com/stacklok/mecatl/internal/executionexecutor"
)

const (
	workspaceRoot  = "/workspace"
	serveMode      = "serve"
	superviseMode  = "supervise-command"
	commandTimeout = 30 * time.Minute
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == superviseMode {
		status := os.NewFile(3, "status")
		err := executionexecutor.SuperviseCommand(os.Stdin, status, os.Stdout, os.Stderr)
		if err != nil {
			os.Exit(1)
		}
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if len(os.Args) > 1 {
		if err := serveCommand(ctx, os.Args[1:]); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "mecatl-executor:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(ctx, os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "mecatl-executor: protocol output failed")
		os.Exit(1)
	}
}

func run(parent context.Context, in io.Reader, out io.Writer) error {
	var resp executionenv.ExecutorResponse
	var opErr error
	if runtime.GOOS != "linux" {
		opErr = errors.New("mecatl-executor requires Linux at runtime")
	} else if err := executionexecutor.EnableCommandExecution(); err != nil {
		opErr = err
	} else {
		reader := bufio.NewReader(&io.LimitedReader{R: in, N: executionenv.MaxJSONBody + 1})
		body, err := reader.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			opErr = err
		} else if len(body) > executionenv.MaxJSONBody {
			opErr = &executionenv.Error{Code: executionenv.CodeResourceExhausted, Message: "executor request exceeds limit"}
		} else {
			var q executionenv.ExecutorRequest
			if err := executionenv.DecodeStrict(body, &q); err != nil {
				opErr = err
			} else {
				limits := executionexecutor.Limits{CommandTimeout: commandTimeout}
				x, err := executionexecutor.New(workspaceRoot, limits)
				if err != nil {
					opErr = err
				} else {
					defer func() { _ = x.Close() }()
					opCtx, cancel := context.WithCancel(parent)
					if q.Operation == executionenv.OpCommandStart {
						go func() {
							var one [1]byte
							_, _ = reader.Read(one[:])
							cancel()
						}()
					}
					resp, opErr = x.Execute(opCtx, q)
					cancel()
				}
			}
		}
	}
	envelope := executionenv.ExecutorEnvelope{}
	if opErr != nil {
		var pe *executionenv.Error
		if !errors.As(opErr, &pe) {
			pe = &executionenv.Error{Code: executionenv.CodeInternal, Message: "executor operation failed"}
		}
		envelope.Error = pe
	} else {
		envelope.Response = &resp
	}
	return json.NewEncoder(out).Encode(envelope)
}
