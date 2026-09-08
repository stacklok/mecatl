package fstools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const removeDescription = `Remove one file or empty physical directory.

Safety:
- Removal is never recursive. A non-empty directory is refused.
- This is a namespace operation and does not require a prior Read.

Arguments:
- path: workspace-relative path to remove.`

// RemoveTool removes one file or empty physical directory non-recursively.
type RemoveTool struct{}

var _ tool.Tool = RemoveTool{}

type removeArgs struct {
	Path string `json:"path"`
}

// Spec returns the model-facing specification of the Remove tool.
func (RemoveTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: "Remove", Description: removeDescription,
		Schema: schema(`{
  "type": "object",
  "properties": {"path": {"type": "string", "description": "Workspace-relative path to remove non-recursively."}},
  "required": ["path"]
}`),
	}
}

// ReadOnly reports that Remove mutates the workspace.
func (RemoveTool) ReadOnly() bool { return false }

// Execute removes one path non-recursively.
func (RemoveTool) Execute(ctx context.Context, in session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	var args removeArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if args.Path == "" {
		return session.NewToolError(in.ID, "the \"path\" argument is required"), nil
	}
	ns, ok := env.Workspace().(tool.WorkspaceNamespace)
	if !ok {
		return session.NewToolError(in.ID, "this workspace does not support removing paths"), nil
	}
	if err := ns.Remove(ctx, args.Path); err != nil {
		switch {
		case errors.Is(err, tool.ErrFileOperationUnsupported):
			return session.NewToolError(in.ID, "this workspace does not support removing paths"), nil
		case errors.Is(err, tool.ErrDirectoryNotEmpty):
			return session.NewToolError(in.ID, fmt.Sprintf("cannot remove %q: directory is not empty; recursive removal is not supported", args.Path)), nil
		case errors.Is(err, fs.ErrNotExist):
			return session.NewToolError(in.ID, fmt.Sprintf("cannot remove %q: path does not exist", args.Path)), nil
		default:
			return session.NewToolError(in.ID, fmt.Sprintf("cannot remove %q: %v", args.Path, err)), nil
		}
	}
	return session.NewToolResult(in.ID, fmt.Sprintf("removed %q", args.Path)), nil
}
