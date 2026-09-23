package fstools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const listDirDescription = `List the immediate children of a directory.

When to use:
- To inspect one directory without recursively globbing the workspace.
- To distinguish immediate files from subdirectories.

Arguments:
- path: directory path relative to the workspace root, or a policy-authorized absolute path when the workspace backend supports it; use "." for the root.

Limits:
- Local external listing does not support the filesystem root "/"; choose a specific external directory.
- Returns at most 1000 entries, sorted. Directories have a trailing "/".
- Some virtual workspaces derive directories from file paths and do not preserve empty directories.`

// ListDirTool lists the immediate children of one directory.
type ListDirTool struct{}

var _ tool.Tool = ListDirTool{}

type listDirArgs struct {
	Path string `json:"path"`
}

// Spec returns the model-facing specification of the ListDir tool.
func (ListDirTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: "ListDir", Description: listDirDescription,
		Schema: schema(`{
  "type": "object",
  "properties": {"path": {"type": "string", "description": "Directory path relative to the workspace root, or a policy-authorized absolute path when supported by the workspace backend; use '.' for the root."}},
  "required": ["path"]
}`),
	}
}

// ReadOnly reports that ListDir does not mutate the workspace.
func (ListDirTool) ReadOnly() bool { return true }

// Execute lists the immediate children of a directory.
func (ListDirTool) Execute(ctx context.Context, in session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	var args listDirArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if args.Path == "" {
		return session.NewToolError(in.ID, "the \"path\" argument is required; use \".\" for the workspace root"), nil
	}
	ns, ok := env.Workspace().(tool.WorkspaceNamespace)
	if !ok {
		return session.NewToolError(in.ID, "this workspace does not support directory listing"), nil
	}
	entries, err := ns.ReadDir(ctx, args.Path)
	if errors.Is(err, tool.ErrFileOperationUnsupported) {
		return session.NewToolError(in.ID, "this workspace does not support directory listing"), nil
	}
	if err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf("cannot list directory %q: %v", args.Path, err)), nil
	}
	if len(entries) == 0 {
		return session.NewToolResult(in.ID, "directory is empty"), nil
	}
	limit := len(entries)
	if limit > maxGlobResults {
		limit = maxGlobResults
	}
	lines := make([]string, 0, limit+1)
	for _, entry := range entries[:limit] {
		name := entry.Name
		if entry.IsDir {
			name += "/"
		}
		lines = append(lines, name)
	}
	if limit < len(entries) {
		lines = append(lines, fmt.Sprintf("... [output truncated: showing first %d entries]", maxGlobResults))
	}
	return session.NewToolResult(in.ID, truncateBytes(strings.Join(lines, "\n")+"\n")), nil
}
