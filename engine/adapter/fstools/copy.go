package fstools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const copyDescription = `Copy one regular file to a new path.

Safety:
- The destination must not exist; Copy never overwrites it.
- Copy does not require a prior Read and does not update the read ledger.
- Directory copying is not supported.

Arguments:
- source: existing workspace-relative regular file.
- destination: new workspace-relative file path.`

// CopyTool copies one regular file to an absent destination.
type CopyTool struct{}

var _ tool.Tool = CopyTool{}

type copyArgs struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// Spec returns the model-facing specification of the Copy tool.
func (CopyTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: "Copy", Description: copyDescription,
		Schema: schema(`{
  "type": "object",
  "properties": {
    "source": {"type": "string", "description": "Existing workspace-relative regular file."},
    "destination": {"type": "string", "description": "New workspace-relative destination path; must not exist."}
  },
  "required": ["source", "destination"]
}`),
	}
}

// ReadOnly reports that Copy mutates the workspace.
func (CopyTool) ReadOnly() bool { return false }

// Execute copies a regular file without overwriting an existing destination.
func (CopyTool) Execute(ctx context.Context, in session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	var args copyArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if args.Source == "" || args.Destination == "" {
		return session.NewToolError(in.ID, "the \"source\" and \"destination\" arguments are required"), nil
	}
	ns, ok := env.Workspace().(tool.WorkspaceNamespace)
	if !ok {
		return session.NewToolError(in.ID, "this workspace does not support copying files"), nil
	}
	if _, err := ns.CopyFile(ctx, args.Source, args.Destination); err != nil {
		switch {
		case errors.Is(err, tool.ErrFileOperationUnsupported):
			return session.NewToolError(in.ID, "this workspace does not support copying files"), nil
		case errors.Is(err, fs.ErrExist):
			return session.NewToolError(in.ID, fmt.Sprintf("cannot copy to %q: destination already exists", args.Destination)), nil
		case errors.Is(err, fs.ErrNotExist):
			return session.NewToolError(in.ID, fmt.Sprintf("cannot copy %q: source does not exist", args.Source)), nil
		case errors.Is(err, fs.ErrInvalid):
			return session.NewToolError(in.ID, fmt.Sprintf("cannot copy %q: source is not a regular file", args.Source)), nil
		default:
			return session.NewToolError(in.ID, fmt.Sprintf("cannot copy %q to %q: %v", args.Source, args.Destination, err)), nil
		}
	}
	return session.NewToolResult(in.ID, fmt.Sprintf("copied %q to %q", args.Source, args.Destination)), nil
}
