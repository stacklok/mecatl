// Command mecatl-executor is the credential-free executor workload.
//
// "serve" runs the executor gRPC server over mutual TLS with the Files and
// Commands services enabled by -files and -commands; "supervise-command" is
// that server's per-command helper.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	if len(os.Args) < 2 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: mecatl-executor serve [flags]")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := runServeMode(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "mecatl-executor:", err)
		os.Exit(1)
	}
}
