// Package gitexec runs the narrow set of host Git operations used by the
// microVM worktree lifecycle without ambient executable extensions.
package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

var fixedArgs = []string{
	"--no-pager",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
	"-c", "core.pager=cat",
}

// Run invokes Git in dir with repository execution hooks and ambient config disabled.
func Run(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	return run(ctx, dir, stdin, nil, args...)
}

// RunWithEnv invokes Git with an additional explicit, non-ambient environment.
// It is used for isolated temporary indexes; callers must supply complete KEY=value entries.
func RunWithEnv(ctx context.Context, dir string, stdin []byte, environment []string, args ...string) ([]byte, error) {
	return run(ctx, dir, stdin, environment, args...)
}

// RunInDir invokes Git with an inherited directory descriptor selected through
// /dev/fd/3 after process start. This binds cleanup to an already-open directory
// identity even if an attacker renames a logical ancestor concurrently.
func RunInDir(ctx context.Context, dir *os.File, stdin []byte, args ...string) ([]byte, error) {
	return runCommand(ctx, "/", []*os.File{dir}, []string{"-C", "/dev/fd/3"}, stdin, nil, args...)
}

func run(ctx context.Context, dir string, stdin []byte, environment []string, args ...string) ([]byte, error) {
	return runCommand(ctx, dir, nil, nil, stdin, environment, args...)
}

// RunStream invokes Git while streaming stdin and stdout. Stderr is bounded so
// repository-controlled path diagnostics cannot consume unbounded host memory.
func RunStream(ctx context.Context, dir string, stdin io.Reader, stdout io.Writer, args ...string) error {
	if len(args) == 0 {
		return errors.New("git command is required")
	}
	commandArgs := commandArguments(nil, args)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	stderr := &limitedBuffer{remaining: 64 << 10}
	cmd.Stderr = stderr
	cmd.Env = commandEnvironment(nil)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, bytes.TrimSpace(stderr.Bytes()))
	}
	return nil
}

func runCommand(ctx context.Context, dir string, extraFiles []*os.File, prefix []string, stdin []byte, environment []string, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("git command is required")
	}
	commandArgs := commandArguments(prefix, args)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	cmd.Dir = dir
	cmd.ExtraFiles = extraFiles
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = commandEnvironment(environment)
	var stdout bytes.Buffer
	stderr := &limitedBuffer{remaining: 64 << 10}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}

func commandArguments(prefix, args []string) []string {
	commandArgs := make([]string, 0, len(fixedArgs)+len(prefix)+len(args)+1)
	commandArgs = append(commandArgs, fixedArgs...)
	commandArgs = append(commandArgs, prefix...)
	commandArgs = append(commandArgs, args[0])
	if args[0] == "diff" {
		commandArgs = append(commandArgs, "--no-ext-diff")
	}
	return append(commandArgs, args[1:]...)
}

func commandEnvironment(environment []string) []string {
	return append([]string{
		"PATH=" + os.Getenv("PATH"),
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"PAGER=cat",
	}, environment...)
}

type limitedBuffer struct {
	bytes.Buffer
	remaining int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	if len(p) > b.remaining {
		p = p[:b.remaining]
	}
	b.remaining -= len(p)
	_, _ = b.Buffer.Write(p)
	return original, nil
}
