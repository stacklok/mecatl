//go:build !unix

package main

import (
	"os"
	"testing"
)

// rawPipe is unavailable without pipe(2). --lifetime-pipe-fd is a POSIX
// inherited-descriptor contract and local spawn on Windows is an explicit
// non-goal of issue #821 — the same boundary socketumask_other.go draws.
func rawPipe(t *testing.T) (int, *os.File) {
	t.Helper()
	t.Skip("--lifetime-pipe-fd needs POSIX pipe(2)")
	return 0, nil
}
