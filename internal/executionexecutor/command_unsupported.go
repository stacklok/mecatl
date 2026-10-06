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

// errCommandUnsupported is a variable, not an inline errors.New, so staticcheck
// cannot prove the stubs' error is always non-nil and flag the shared callers'
// err != nil checks (SA4023) on non-Linux builds.
var errCommandUnsupported = errors.New("command execution requires Linux child-subreaper support")

// EnableCommandExecution reports that the workload helper is Linux-only.
func EnableCommandExecution() error {
	return errCommandUnsupported
}

func runIsolatedCommand(context.Context, string, string, int) (commandResult, bool, error) {
	return commandResult{}, false, errCommandUnsupported
}
