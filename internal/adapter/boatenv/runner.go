package boatenv

import (
	"context"
	"errors"
	"strings"

	"github.com/stacklok/mecatl/engine/tool"
)

type runner struct {
	client    *apiClient
	sandboxID string
	workdir   string
	root      string
}

var _ tool.CommandRunner = (*runner)(nil)

// BoundWorkspaceRoot is the placement-binding affinity proof consumed by the
// server. It exactly matches the sibling workspace's logical Root value.
func (r *runner) BoundWorkspaceRoot() string { return r.root }

func (r *runner) Run(ctx context.Context, command string) (tool.CommandResult, error) {
	if strings.TrimSpace(command) == "" {
		return tool.CommandResult{}, errors.New("boatenv: empty command")
	}
	out, err := r.client.runCommand(ctx, r.sandboxID, r.workdir, command)
	result := tool.CommandResult{Stdout: out.Stdout, Stderr: out.Stderr, ExitCode: out.ExitCode}
	if err != nil {
		return result, err
	}
	return result, nil
}
