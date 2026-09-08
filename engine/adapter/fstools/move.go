package fstools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const moveDescription = `Move a file or directory to a new path.

Safety:
- The destination must not exist; Move never overwrites it.
- This is a namespace operation and does not require a prior Read.

Arguments:
- source: existing workspace-relative file or directory path.
- destination: new workspace-relative path.`

// MoveTool moves a file or directory to an absent destination.
type MoveTool struct{}

var _ tool.Tool = MoveTool{}

type moveArgs struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// Spec returns the model-facing specification of the Move tool.
func (MoveTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: "Move", Description: moveDescription,
		Schema: schema(`{
  "type": "object",
  "properties": {
    "source": {"type": "string", "description": "Existing workspace-relative file or directory path."},
    "destination": {"type": "string", "description": "New workspace-relative destination path; must not exist."}
  },
  "required": ["source", "destination"]
}`),
	}
}

// ReadOnly reports that Move mutates the workspace.
func (MoveTool) ReadOnly() bool { return false }

// Execute moves a path without overwriting an existing destination.
func (MoveTool) Execute(ctx context.Context, in session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	var args moveArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if args.Source == "" || args.Destination == "" {
		return session.NewToolError(in.ID, "the \"source\" and \"destination\" arguments are required"), nil
	}
	ns, ok := env.Workspace().(tool.WorkspaceNamespace)
	if !ok {
		return session.NewToolError(in.ID, "this workspace does not support moving paths"), nil
	}
	if err := ns.Rename(ctx, args.Source, args.Destination); err != nil {
		switch {
		case errors.Is(err, tool.ErrFileOperationUnsupported):
			return session.NewToolError(in.ID, "this workspace does not support moving paths"), nil
		case errors.Is(err, fs.ErrExist):
			return session.NewToolError(in.ID, fmt.Sprintf("cannot move to %q: destination already exists", args.Destination)), nil
		case errors.Is(err, fs.ErrNotExist):
			return session.NewToolError(in.ID, fmt.Sprintf("cannot move %q: source does not exist", args.Source)), nil
		default:
			return session.NewToolError(in.ID, fmt.Sprintf("cannot move %q to %q: %v", args.Source, args.Destination, err)), nil
		}
	}
	return session.NewToolResult(in.ID, fmt.Sprintf("moved %q to %q", args.Source, args.Destination)), nil
}
