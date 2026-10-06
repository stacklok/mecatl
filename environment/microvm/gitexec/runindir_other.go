//go:build !darwin

package gitexec

import (
	"context"
	"os"
)

func runInDir(ctx context.Context, dir *os.File, stdin []byte, args ...string) ([]byte, error) {
	return runCommand(ctx, "/", []*os.File{dir}, []string{"-C", "/dev/fd/3"}, stdin, nil, args...)
}
