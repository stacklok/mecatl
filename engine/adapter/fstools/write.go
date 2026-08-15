package fstools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// writeDescription is the model-facing documentation for the Write tool.
const writeDescription = `Write content to a file, creating it or fully replacing its contents.

When to use:
- To create a new file.
- To completely rewrite an existing file when a targeted Edit is impractical.

When NOT to use:
- To change part of an existing file: prefer Edit, which is safer and smaller.

Read-before-overwrite:
- Creating a NEW file requires no prior read.
- Overwriting an EXISTING file requires that you read it this session and it is
  unchanged since — the same protection Edit enforces, so you never silently
  clobber concurrent changes. If the file changed, re-read it, then retry.

Arguments:
- path    (required): workspace-relative path to write. Absolute paths that
  resolve inside the workspace root are accepted.
- content (required): the full new file contents.

Example:
  {"path": "notes/todo.txt", "content": "buy milk\n"}

Limits:
- Parent directories are created as needed. Writing replaces the whole file; for
  surgical changes use Edit.`

// WriteTool writes a file. Creating a new file needs no prior read; overwriting
// an existing file requires a read-before-overwrite, mirroring Edit invariant
// #1. It mutates state, so ReadOnly is false.
type WriteTool struct{}

// Compile-time assertion that WriteTool implements tool.Tool.
var _ tool.Tool = WriteTool{}

// writeArgs is the JSON argument shape for the Write tool.
type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Spec returns the model-facing specification of the Write tool.
func (WriteTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        "Write",
		Description: writeDescription,
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Workspace-relative path to write. Absolute paths that resolve inside the workspace root are accepted."},
    "content": {"type": "string", "description": "Full new contents of the file."}
  },
  "required": ["path", "content"]
}`),
	}
}

// ReadOnly reports that Write mutates state.
func (WriteTool) ReadOnly() bool { return false }

// Execute writes the file, enforcing read-before-overwrite on existing paths
// via the version protocol (ADR 0208): a NEW file uses create-only; an
// EXISTING file requires a recorded version, re-reads the current version, and
// finishes with a conditional replace against that current version. No
// unconditional operation is used by the agent-facing Write tool.
func (WriteTool) Execute(ctx context.Context, in session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	ws := env.Workspace()
	var args writeArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if args.Path == "" {
		return session.NewToolError(in.ID, "the \"path\" argument is required"), nil
	}

	overwriteConflict := session.NewToolError(in.ID, fmt.Sprintf(
		"refusing to overwrite existing file %q: it was not read this session, or it changed since you read it. Read it first, then retry.",
		args.Path))

	// Determine whether the path already exists.
	_, statErr := ws.Stat(ctx, args.Path)
	switch {
	case statErr == nil:
		// Existing file: require read-before-overwrite via the version protocol.
		recorded, recordedOK := ws.RecordedVersion(args.Path)
		if !recordedOK {
			return overwriteConflict, nil
		}
		_, current, err := ws.ReadVersion(ctx, args.Path)
		if err != nil {
			// A read failure is a model-visible "cannot read" error, NOT the
			// changed-since-read refusal — the file may have been deleted or made
			// unreadable since the recorded read; surface the real cause.
			return session.NewToolError(in.ID, fmt.Sprintf("cannot read %q: %v", args.Path, err)), nil
		}
		if !recorded.Equal(current) {
			return overwriteConflict, nil
		}
		newVer, err := ws.ReplaceFile(ctx, args.Path, current, []byte(args.Content))
		if err != nil {
			var mismatch *tool.VersionMismatchError
			if errors.As(err, &mismatch) {
				return overwriteConflict, nil
			}
			// A concurrent DELETE after the current read but before this replace
			// surfaces as fs.ErrNotExist -> a model-visible "deleted since you
			// read it" refusal, distinct from a concurrent change. An unrelated
			// write failure stays a harness-level error.
			if errors.Is(err, fs.ErrNotExist) {
				return session.NewToolError(in.ID, fmt.Sprintf(
					"refusing to overwrite %q: it was deleted since you read it. Read it again to confirm, then retry.",
					args.Path)), nil
			}
			return session.ToolResult{}, fmt.Errorf("write: writing %q: %w", args.Path, err)
		}
		// Re-record the new version so a subsequent Edit/Write in the same turn is valid.
		ws.RecordRead(args.Path, newVer)
		return session.NewToolResult(in.ID, fmt.Sprintf("overwrote %q (%d bytes)", args.Path, len(args.Content))), nil
	case errors.Is(statErr, fs.ErrNotExist):
		// New file: create-only (no prior read needed). A concurrent creation
		// surfaces as fs.ErrExist -> a create-conflict refusal.
		newVer, err := ws.CreateFile(ctx, args.Path, []byte(args.Content))
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				return session.NewToolError(in.ID, fmt.Sprintf(
					"cannot create %q: a file already exists at that path. Read it first, then overwrite it.",
					args.Path)), nil
			}
			return session.ToolResult{}, fmt.Errorf("write: creating %q: %w", args.Path, err)
		}
		ws.RecordRead(args.Path, newVer)
		return session.NewToolResult(in.ID, fmt.Sprintf("wrote %q (%d bytes)", args.Path, len(args.Content))), nil
	default:
		return session.ToolResult{}, fmt.Errorf("write: stat %q: %w", args.Path, statErr)
	}
}
