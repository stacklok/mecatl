package boatenv

import (
	"context"
	"errors"
	"strings"

	"github.com/stacklok/mecatl/engine/tool"
)

type runner struct {
	sandbox *sandbox
	root    string
}

var _ tool.CommandRunner = (*runner)(nil)

// BoundWorkspaceRoot is the placement-binding affinity proof consumed by the
// server. It exactly matches the sibling workspace's logical Root value.
func (r *runner) BoundWorkspaceRoot() string { return r.root }

// Run executes command in the sandbox workdir. The time limit follows ctx
// (clamped to the API's 1-600s range); a signal-killed process reports -1.
// Output Boat truncated at its per-stream cap is flagged on stderr rather
// than passed off as complete.
func (r *runner) Run(ctx context.Context, command string) (tool.CommandResult, error) {
	if strings.TrimSpace(command) == "" {
		return tool.CommandResult{}, errors.New("boatenv: empty command")
	}
	out, err := r.sandbox.run(ctx, command)
	stderr := out.Stderr
	if out.StdoutTruncated || out.StderrTruncated {
		if stderr != "" && !strings.HasSuffix(stderr, "\n") {
			stderr += "\n"
		}
		stderr += "[boatenv: output truncated by the sandbox at its per-stream limit]"
	}
	return tool.CommandResult{Stdout: out.Stdout, Stderr: stderr, ExitCode: out.ExitCode}, err
}
