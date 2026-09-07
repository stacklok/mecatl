package fstools

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// grepDescription is the model-facing documentation for the Grep tool.
const grepDescription = `Search file contents for a regular expression and return matching lines with their file path and line number.

When to use:
- To find where a symbol, string, or pattern appears across the workspace.
- To locate the definition or usages of a function before reading or editing.

When NOT to use:
- To find files by name (use Glob) or to read a whole file (use Read).

Arguments:
- pattern (required): a regular expression (RE2 syntax) to match per line.
- path    (optional): a glob restricting which files are searched; empty means
  search all files. A leading "/" is stripped (the glob is root-relative, not an
  absolute path).

Example:
  {"pattern": "func New[A-Z]", "path": "internal/**/*.go"}

Limits:
- Returns at most 200 matches per call; beyond that the result is truncated with
  a marker — narrow the pattern or path. Broad unscoped searches also have a
  safety budget and may require a narrower path. Binary files are skipped.`

// GrepTool searches file contents for a regular expression. It does not mutate
// state, so ReadOnly is true.
type GrepTool struct{}

// Compile-time assertion that GrepTool implements tool.Tool.
var _ tool.Tool = GrepTool{}

// grepArgs is the JSON argument shape for the Grep tool.
type grepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

// Spec returns the model-facing specification of the Grep tool.
func (GrepTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        "Grep",
		Description: grepDescription,
		Schema: schema(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "RE2 regular expression to match per line."},
    "path": {"type": "string", "description": "Optional glob restricting which files are searched (root-relative; a leading '/' is stripped)."}
  },
  "required": ["pattern"]
}`),
		// path is intentionally optional/absent from "required" — fine because the
		// openai adapter sends tools NON-STRICT (see bash.go's note); don't add it.
	}
}

// ReadOnly reports that Grep does not mutate state.
func (GrepTool) ReadOnly() bool { return true }

// Execute runs the search and returns capped, formatted matches.
func (GrepTool) Execute(ctx context.Context, in session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	ws := env.Workspace()
	var args grepArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if args.Pattern == "" {
		return session.NewToolError(in.ID, "the \"pattern\" argument is required"), nil
	}

	matches, err := ws.Grep(ctx, args.Pattern, args.Path)
	if err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf("grep failed: %v", err)), nil
	}
	if len(matches) == 0 {
		return session.NewToolResult(in.ID, "no matches"), nil
	}

	truncated := false
	if len(matches) > maxGrepMatches {
		matches = matches[:maxGrepMatches]
		truncated = true
	}

	var b strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&b, "%s:%d:%s\n", m.Path, m.Line, m.Text)
	}
	if truncated {
		fmt.Fprintf(&b, "... [output truncated: showing first %d matches; narrow the pattern or path]\n", maxGrepMatches)
	}
	return session.NewToolResult(in.ID, truncateBytes(b.String())), nil
}
