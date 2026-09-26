//go:build !linux

package executionexecutor

import (
	"context"
	"errors"
)

type commandResult struct {
	stdout    []byte
	stderr    []byte
	exitCode  int
	truncated bool
}

// EnableCommandExecution reports that the workload helper is Linux-only.
func EnableCommandExecution() error {
	return errors.New("command execution requires Linux child-subreaper support")
}

func runIsolatedCommand(context.Context, string, string, int) (commandResult, bool, error) {
	return commandResult{}, false, errors.New("command execution requires Linux child-subreaper support")
}
