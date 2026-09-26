//go:build darwin

package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const darwinFchdirScript = `import os, sys
os.fchdir(3)
os.execv(sys.argv[1], sys.argv[1:])
`

func runInDir(ctx context.Context, dir *os.File, stdin []byte, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("git command is required")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("locate git: %w", err)
	}
	gitPath, err = filepath.Abs(gitPath)
	if err != nil {
		return nil, fmt.Errorf("resolve git: %w", err)
	}
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		return nil, fmt.Errorf("locate python3: %w", err)
	}
	gitArgs := commandArguments(nil, args)
	launcherArgs := make([]string, 0, len(gitArgs)+5)
	launcherArgs = append(launcherArgs, "-I", "-S", "-c", darwinFchdirScript, gitPath)
	launcherArgs = append(launcherArgs, gitArgs...)
	cmd := exec.CommandContext(ctx, pythonPath, launcherArgs...)
	cmd.Dir = "/"
	cmd.ExtraFiles = []*os.File{dir}
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = commandEnvironment(nil)
	var stdout bytes.Buffer
	stderr := &limitedBuffer{remaining: 64 << 10}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}
