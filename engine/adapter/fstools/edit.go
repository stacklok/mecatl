package fstools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// editDescription is the model-facing documentation for the Edit tool.
const editDescription = `Replace an exact string in a file with new text. This is the primary tool for modifying existing files.

Three invariants are enforced and any violation is reported back so you can fix it:
1. Read-before-edit: you must have read the file with the Read tool this session,
   and it must be unchanged since you read it. If it changed (or you never read
   it), re-read it first, then retry.
2. Exact match: old_string must appear in the file byte-for-byte, including
   whitespace and indentation. Do NOT include the line-number prefixes that the
   Read tool adds to its output.
3. Uniqueness: old_string must match exactly once, unless replace_all is true.
   If it matches more than once and replace_all is false, the edit is rejected;
   add surrounding context to old_string to make it unique, or set replace_all.

When to use:
- To change part of an existing file. To create a new file, use Write instead.

Arguments:
- path        (required): workspace-relative path to the file. Absolute paths
  that resolve inside the workspace root are accepted.
- old_string  (required): the exact text to replace.
- new_string  (required): the replacement text (may be empty to delete).
- replace_all (optional): replace every occurrence instead of requiring uniqueness.

Example:
  {"path": "main.go", "old_string": "fmt.Println(\"hi\")", "new_string": "fmt.Println(\"hello\")"}

Limits:
- old_string and new_string must differ. Editing a binary file is not supported.`

// EditTool replaces an exact substring in a file, enforcing the three Edit
// invariants (read-before-edit, exact-match, uniqueness) against the Workspace
// read-ledger. It mutates state, so ReadOnly is false.
type EditTool struct{}

// Compile-time assertion that EditTool implements tool.Tool.
var _ tool.Tool = EditTool{}

// editArgs is the JSON argument shape for the Edit tool.
type editArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// Spec returns the model-facing specification of the Edit tool.
func (EditTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        "Edit",
		Description: editDescription,
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Workspace-relative path to the file to edit. Absolute paths that resolve inside the workspace root are accepted."},
    "old_string": {"type": "string", "description": "Exact text to replace (no line-number prefixes)."},
    "new_string": {"type": "string", "description": "Replacement text; may be empty to delete."},
    "replace_all": {"type": "boolean", "description": "Replace every occurrence instead of requiring a unique match."}
  },
  "required": ["path", "old_string", "new_string"]
}`),
		// replace_all is intentionally optional/absent from "required" — fine because
		// the openai adapter sends tools NON-STRICT (see bash.go's note); don't add it.
	}
}

// ReadOnly reports that Edit mutates state.
func (EditTool) ReadOnly() bool { return false }

// Execute enforces the three Edit invariants and writes the modified file via a
// conditional replace (ADR 0208).
func (EditTool) Execute(ctx context.Context, in session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	ws := env.Workspace()
	var args editArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if args.Path == "" {
		return session.NewToolError(in.ID, "the \"path\" argument is required"), nil
	}
	if args.OldString == "" {
		return session.NewToolError(in.ID, "the \"old_string\" argument is required; to create a new file use the Write tool"), nil
	}
	if args.OldString == args.NewString {
		return session.NewToolError(in.ID, "\"old_string\" and \"new_string\" are identical; nothing to change"), nil
	}

	readConflict := session.NewToolError(in.ID, fmt.Sprintf(
		"refusing to edit %q: it was not read this session, or it changed since you read it. Read the file again, then retry the edit.",
		args.Path))

	// Invariant #1a: read-before-edit. RecordedVersion is an I/O-free lookup of
	// the version a prior Read recorded; ok=false means the file was not read
	// this session. A non-nil err means the ledger lookup itself is
	// UNAVAILABLE or CORRUPT (ADR 0281) — DISTINCT from ordinary absence — and
	// must refuse BEFORE ReplaceFile is ever called; it is never treated as an
	// unrecorded-but-otherwise-authorized read.
	ledger := env.ReadLedger()
	recorded, recordedOK, err := ledger.RecordedVersion(ctx, tool.LedgerKey(ws.Root(), args.Path))
	if err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf(
			"refusing to edit %q: could not verify it was read this session (%v). Read the file again, then retry the edit.",
			args.Path, err)), nil
	}
	if !recordedOK {
		return readConflict, nil
	}

	// Re-read the file with its CURRENT authoritative version. This is the
	// version-bearing read that also yields the content the edit is computed
	// against. A read failure is a model-visible "cannot read" error, NOT the
	// changed-since-read refusal — the file may have been deleted, made
	// unreadable, or the path rejected; none of those are "changed since you
	// read it", and surfacing the real cause lets the model act on it.
	data, current, err := ws.ReadVersion(ctx, args.Path)
	if err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf("cannot read %q: %v", args.Path, err)), nil
	}

	// Invariant #1b: unchanged-since-read. The recorded version (from the prior
	// Read) must equal the current version; a mismatch means the file changed
	// since the read.
	if !recorded.Equal(current) {
		return readConflict, nil
	}

	content := string(data)

	// Invariant #2: exact match.
	count := strings.Count(content, args.OldString)
	if count == 0 {
		return session.NewToolError(in.ID, fmt.Sprintf(
			"old_string was not found in %q. It must match the file exactly (including whitespace) and must not include the line-number prefixes Read adds.",
			args.Path)), nil
	}

	// Invariant #3: uniqueness unless replace_all.
	if count > 1 && !args.ReplaceAll {
		return session.NewToolError(in.ID, fmt.Sprintf(
			"old_string matched %d times in %q; it must be unique. Add surrounding context to make it unique, or set replace_all=true to replace every occurrence.",
			count, args.Path)), nil
	}

	var updated string
	if args.ReplaceAll {
		updated = strings.ReplaceAll(content, args.OldString, args.NewString)
	} else {
		updated = strings.Replace(content, args.OldString, args.NewString, 1)
	}

	// Finish with a CONDITIONAL replace against the CURRENT version (the CAS).
	// A concurrent mutation that landed between the ReadVersion above and this
	// ReplaceFile surfaces as a VersionMismatchError -> the same "changed since
	// you read it" refusal, so the final CAS is load-bearing and model-visible.
	// A concurrent DELETE after the current read but before this replace
	// surfaces as fs.ErrNotExist -> a model-visible "deleted since you read it"
	// refusal, distinct from a concurrent change (the file is GONE, not
	// changed). An unrelated write failure stays a harness-level error.
	newVer, err := ws.ReplaceFile(ctx, args.Path, current, []byte(updated))
	if err != nil {
		var mismatch *tool.VersionMismatchError
		if errors.As(err, &mismatch) {
			return readConflict, nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			return session.NewToolError(in.ID, fmt.Sprintf(
				"refusing to edit %q: it was deleted since you read it. Read it again to confirm, then retry.",
				args.Path)), nil
		}
		return session.ToolResult{}, fmt.Errorf("edit: writing %q: %w", args.Path, err)
	}

	replaced := 1
	if args.ReplaceAll {
		replaced = count
	}

	// Re-record the new version so subsequent edits in the same turn remain
	// valid. The edit ALREADY SUCCEEDED (ReplaceFile above); a failure here is
	// reported honestly WITHOUT rollback and establishes no new evidence. Any
	// older evidence retains only its exact-version meaning (ADR 0281).
	if err := ledger.RecordRead(ctx, tool.LedgerKey(ws.Root(), args.Path), newVer); err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf(
			"edited %q: replaced %d occurrence(s), but failed to retain read evidence for the new version: %v. No new evidence was stored; any earlier evidence remains subject to version checks.",
			args.Path, replaced, err)), nil
	}
	return session.NewToolResult(in.ID, fmt.Sprintf("edited %q: replaced %d occurrence(s)", args.Path, replaced)), nil
}
