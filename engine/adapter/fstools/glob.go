package fstools

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// globDescription is the model-facing documentation for the Glob tool.
const globDescription = `Find files whose paths match a shell-style glob pattern.

When to use:
- To discover files by name or location, e.g. all Go files or all tests.
- As a first step before reading or grepping when you don't yet know the paths.

When NOT to use:
- To search file contents (use Grep) or to read a file (use Read).

Arguments:
- pattern (required): a shell-style glob, e.g. "*.go", "cmd/*/main.go".
  "*" matches within a single path segment; "**" matches across directories
  recursively, so "**/*.go" finds every Go file at any depth under the root.
  A leading "/" is stripped (patterns are root-relative, not absolute paths).

Example:
  {"pattern": "internal/adapter/*/*.go"}
  {"pattern": "**/*.go"}

Limits:
- Returns at most 1000 paths per call, sorted; beyond that the list is truncated
  with a marker — use a more specific pattern.`

// GlobTool lists files matching a glob pattern. It does not mutate state, so
// ReadOnly is true.
type GlobTool struct{}

// Compile-time assertion that GlobTool implements tool.Tool.
var _ tool.Tool = GlobTool{}

// globArgs is the JSON argument shape for the Glob tool.
type globArgs struct {
	Pattern string `json:"pattern"`
}

// Spec returns the model-facing specification of the Glob tool.
func (GlobTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        "Glob",
		Description: globDescription,
		Schema: schema(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Shell-style glob pattern to match file paths (root-relative; a leading '/' is stripped)."}
  },
  "required": ["pattern"]
}`),
	}
}

// ReadOnly reports that Glob does not mutate state.
func (GlobTool) ReadOnly() bool { return true }

// Execute runs the glob and returns capped, sorted paths.
func (GlobTool) Execute(ctx context.Context, in session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	ws := env.Workspace()
	var args globArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if args.Pattern == "" {
		return session.NewToolError(in.ID, "the \"pattern\" argument is required"), nil
	}

	paths, err := ws.Glob(ctx, args.Pattern)
	if err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf("glob failed: %v", err)), nil
	}
	if len(paths) == 0 {
		return session.NewToolResult(in.ID, "no files match"), nil
	}

	truncated := false
	if len(paths) > maxGlobResults {
		paths = paths[:maxGlobResults]
		truncated = true
	}

	out := strings.Join(paths, "\n") + "\n"
	if truncated {
		out += fmt.Sprintf("... [output truncated: showing first %d paths; use a more specific pattern]\n", maxGlobResults)
	}
	return session.NewToolResult(in.ID, truncateBytes(out)), nil
}
