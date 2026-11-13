//go:build !linux

package executionexecutor

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
)

// SuperviseCommand reports that command supervision is Linux-only.
func SuperviseCommand(io.Reader, io.Writer, *os.File, *os.File) error {
	return errCommandUnsupported
}

// CommandSupervisor refuses every command off Linux.
type CommandSupervisor struct{}

// NewCommandSupervisor returns a supervisor that refuses every command.
func NewCommandSupervisor([]string, []string, string) *CommandSupervisor {
	return &CommandSupervisor{}
}

// Start implements commandserver.Runner.
func (*CommandSupervisor) Start(
	context.Context, string, io.Writer, io.Writer,
) (commandserver.Process, error) {
	return nil, errCommandUnsupported
}

// ReapOrphans returns when ctx ends.
func (*CommandSupervisor) ReapOrphans(ctx context.Context, _ time.Duration) { <-ctx.Done() }
