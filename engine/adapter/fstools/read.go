package fstools

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// readDescription is the model-facing documentation for the Read tool.
const readDescription = `Read a file from the workspace and return its contents with 1-based line-number prefixes.

When to use:
- Before editing any file. The Edit and Write tools require that a file was read
  this session and is unchanged; reading first satisfies that invariant.
- To inspect source, config, or data files.

When NOT to use:
- To find files by name (use Glob) or to search file contents (use Grep).
- For directories: Read targets a single file, not a directory listing.

Output format:
- Each line is prefixed with its 1-based line number and a tab, e.g. "   1\thello".
  The line numbers are display aids; do not copy them into Edit's old_string.

Arguments:
- path     (required): workspace-relative path to the file. Absolute paths that
  resolve inside the workspace root are accepted (the same file a relative path
  reaches). Any other absolute path is rejected.
- offset   (optional): 1-based line number to start reading from.
- limit    (optional): maximum number of lines to return.

Example:
  {"path": "cmd/main.go", "offset": 1, "limit": 50}
  reads the first 50 lines of cmd/main.go.

Limits:
- Returns at most ~2000 lines and ~25000 bytes per call; output past those caps
  is truncated with a marker. Use offset/limit to page through large files.`

// ReadTool reads a file and returns its contents with 1-based line-number
// prefixes, recording the read in the Workspace ledger so Edit's
// read-before-edit invariant can later be satisfied.
type ReadTool struct{}

// Compile-time assertion that ReadTool implements tool.Tool.
var _ tool.Tool = ReadTool{}

// readArgs is the JSON argument shape for the Read tool.
type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// Spec returns the model-facing specification of the Read tool.
func (ReadTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        "Read",
		Description: readDescription,
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Workspace-relative path to the file to read. Absolute paths that resolve inside the workspace root are accepted; any other absolute path is rejected."},
    "offset": {"type": "integer", "description": "1-based line number to start reading from."},
    "limit": {"type": "integer", "description": "Maximum number of lines to return."}
  },
  "required": ["path"]
}`),
		// offset/limit are intentionally optional/absent from "required" — fine because
		// the openai adapter sends tools NON-STRICT (see bash.go's note); don't add them.
	}
}

// ReadOnly reports that Read does not mutate state.
func (ReadTool) ReadOnly() bool { return true }

// Execute reads the file, returns it with line-number prefixes, and records the
// read in the Workspace ledger.
func (ReadTool) Execute(ctx context.Context, in session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	ws := env.Workspace()
	var args readArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if args.Path == "" {
		return session.NewToolError(in.ID, "the \"path\" argument is required"), nil
	}
	if args.Offset < 0 || args.Limit < 0 {
		return session.NewToolError(in.ID, "\"offset\" and \"limit\" must be non-negative"), nil
	}

	data, ver, err := ws.ReadVersion(ctx, args.Path)
	if err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf("cannot read %q: %v", args.Path, err)), nil
	}

	// Record the read with the ADAPTER-MINTED authoritative version (ReadVersion
	// returned it) in the Environment's selected ledger (ADR 0281). It performs
	// NO file-content I/O; it stores this exact token so a later Edit/Write can
	// assert read-before-mutate. A failed record is reported and establishes no
	// new evidence; any older evidence keeps only its exact-version meaning.
	ledger := env.ReadLedger()
	if err := ledger.RecordRead(ctx, tool.LedgerKey(ws.Root(), args.Path), ver); err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf(
			"read %q, but failed to retain new read evidence: %v. Any earlier evidence remains usable only if it still matches the current file version.",
			args.Path, err)), nil
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	// A trailing newline yields a spurious empty final element; drop it so line
	// numbering matches the file's actual line count.
	if len(lines) > 0 && lines[len(lines)-1] == "" && strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}

	total := len(lines)
	start := 0
	if args.Offset > 0 {
		start = args.Offset - 1
	}
	if start > total {
		start = total
	}
	end := total
	if args.Limit > 0 && start+args.Limit < end {
		end = start + args.Limit
	}

	truncatedByLineCap := false
	if end-start > maxReadLines {
		end = start + maxReadLines
		truncatedByLineCap = true
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		// 1-based line number, right-aligned in a 6-wide field, then a tab.
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, lines[i])
	}
	out := b.String()
	if truncatedByLineCap {
		out += fmt.Sprintf("... [output truncated: showed %d lines, more remain; use offset to continue]\n", maxReadLines)
	}
	if out == "" {
		out = "(file is empty)"
	}
	return session.NewToolResult(in.ID, truncateBytes(out)), nil
}
