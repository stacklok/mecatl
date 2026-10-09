package boatenv

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/stacklok/mecatl/engine/tool"
)

// helperProgram is the guest-side filesystem helper. It runs with the
// sandbox's python3 in isolated mode (-I), so files in the workspace can
// never shadow the modules it imports.
//
//go:embed helper.py
var helperProgram string

var helperProgramB64 = base64.StdEncoding.EncodeToString([]byte(helperProgram))

const (
	// inlineRequestLimit keeps the whole command well under the guest's
	// 128 KiB single-argument limit (the command runs as one argv string);
	// larger requests travel through the files API instead.
	inlineRequestLimit = 64 << 10
	// inlineReadLimit is the largest file returned directly on stdout.
	// Larger reads come back as a guest-side snapshot fetched in ranges.
	inlineReadLimit = 2 << 20
	// rangeChunk bounds one range response well under the 8 MiB stdout cap.
	rangeChunk = 3 << 20
	// maxReadBytes bounds a single Read or ReadVersion.
	maxReadBytes = 256 << 20
)

// ErrPathEscape reports a path that resolves outside the workspace,
// including through a symlink.
var ErrPathEscape = errors.New("boatenv: path escapes the Boat workspace")

type helperRequest struct {
	Op            string   `json:"op"`
	Path          string   `json:"path,omitempty"`
	OldPath       string   `json:"old_path,omitempty"`
	NewPath       string   `json:"new_path,omitempty"`
	Base          string   `json:"base,omitempty"`
	Content       string   `json:"content,omitempty"`
	Expected      string   `json:"expected,omitempty"`
	ExpectedValid bool     `json:"expected_valid,omitempty"`
	Snapshot      string   `json:"snapshot,omitempty"`
	Offset        int64    `json:"offset,omitempty"`
	Length        int64    `json:"length,omitempty"`
	Drop          bool     `json:"drop,omitempty"`
	InlineLimit   int64    `json:"inline_limit,omitempty"`
	MaxBytes      int64    `json:"max_bytes,omitempty"`
	MaxEntries    int      `json:"max_entries,omitempty"`
	MaxDepth      int      `json:"max_depth,omitempty"`
	Pattern       string   `json:"pattern,omitempty"`
	Literal       string   `json:"literal,omitempty"`
	Files         []string `json:"files,omitempty"`
	MaxMatches    int      `json:"max_matches,omitempty"`
	MaxFiles      int      `json:"max_files,omitempty"`
	LockDir       string   `json:"lock_dir,omitempty"`
}

type helperResponse struct {
	OK       bool          `json:"ok"`
	Code     string        `json:"code,omitempty"`
	Detail   string        `json:"detail,omitempty"`
	Content  string        `json:"content,omitempty"`
	Version  string        `json:"version,omitempty"`
	Snapshot string        `json:"snapshot,omitempty"`
	Spill    string        `json:"spill,omitempty"`
	Size     int64         `json:"size,omitempty"`
	Info     *helperInfo   `json:"info,omitempty"`
	Entries  []helperInfo  `json:"entries,omitempty"`
	Tree     []treeEntry   `json:"tree,omitempty"`
	Matches  []helperMatch `json:"matches,omitempty"`
	Target   string        `json:"target,omitempty"`
	Root     string        `json:"root,omitempty"`
}

// treeEntry is one lstat record of a tree listing: path, type (d/f/l/o),
// size, permission bits, mtime, and for a symlink the in-workspace kind it
// resolves to ("" when dangling or escaping).
type treeEntry struct {
	Path   string `json:"p"`
	Type   string `json:"t"`
	Size   int64  `json:"s"`
	Perm   uint32 `json:"m"`
	MTime  int64  `json:"n"`
	Target string `json:"k,omitempty"`
}

type helperMatch struct {
	Path string `json:"p"`
	Line int    `json:"l"`
	Text string `json:"x"`
}

// helperSemanticError is a well-formed refusal from the guest helper.
type helperSemanticError struct{ code, detail string }

func (e helperSemanticError) Error() string {
	if e.detail != "" {
		return "boatenv: filesystem helper rejected operation: " + e.code + ": " + e.detail
	}
	return "boatenv: filesystem helper rejected operation: " + e.code
}

// helper runs one operation in the guest. Requests too large for a command
// line are staged through the files API; responses too large for one
// command's stdout are spilled to a guest snapshot and fetched in ranges.
func (w *workspace) helper(ctx context.Context, req helperRequest) (helperResponse, error) {
	req.LockDir = w.lockDir
	raw, err := w.helperRaw(ctx, req)
	if err != nil {
		return helperResponse{}, err
	}
	var out helperResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return helperResponse{}, errors.New("boatenv: filesystem helper returned invalid JSON")
	}
	if out.Spill != "" {
		spilled, err := w.fetchSnapshot(ctx, out.Spill, out.Size)
		if err != nil {
			return helperResponse{}, err
		}
		out = helperResponse{}
		if err := json.Unmarshal(spilled, &out); err != nil {
			return helperResponse{}, errors.New("boatenv: filesystem helper returned invalid spilled JSON")
		}
	}
	if !out.OK {
		return out, helperSemanticError{code: out.Code, detail: sanitizeDetail(out.Detail)}
	}
	return out, nil
}

// helperRaw runs one helper invocation and returns its stdout unparsed.
func (w *workspace) helperRaw(ctx context.Context, req helperRequest) ([]byte, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("boatenv: encode helper request: %w", err)
	}
	arg := base64.StdEncoding.EncodeToString(payload)
	if len(arg) > inlineRequestLimit {
		parts, err := w.sandbox.stage(ctx, payload)
		if err != nil {
			return nil, err
		}
		staged, err := json.Marshal(map[string][]string{"stage": parts})
		if err != nil {
			return nil, fmt.Errorf("boatenv: encode staged request: %w", err)
		}
		arg = base64.StdEncoding.EncodeToString(staged)
	}
	command := "python3 -I -c 'import base64;exec(base64.b64decode(\"" + helperProgramB64 + "\"))' '" + arg + "'"
	result, err := w.sandbox.run(ctx, command)
	if err != nil {
		return nil, err
	}
	if result.StdoutTruncated {
		return nil, errors.New("boatenv: filesystem helper output was truncated by the sandbox")
	}
	if result.ExitCode != 0 {
		detail := sanitizeDetail(lastLine(result.Stderr))
		if detail != "" {
			return nil, fmt.Errorf("boatenv: filesystem helper failed with exit code %d: %s", result.ExitCode, detail)
		}
		return nil, fmt.Errorf("boatenv: filesystem helper failed with exit code %d", result.ExitCode)
	}
	return []byte(strings.TrimSpace(result.Stdout)), nil
}

// fetchSnapshot reads a guest snapshot in bounded ranges; the final range
// deletes it.
func (w *workspace) fetchSnapshot(ctx context.Context, snapshot string, size int64) ([]byte, error) {
	if size < 0 || size > maxReadBytes {
		_, _ = w.helperRaw(ctx, helperRequest{Op: "drop", Snapshot: snapshot, LockDir: w.lockDir})
		return nil, fmt.Errorf("boatenv: helper output of %d bytes exceeds the %d byte limit", size, maxReadBytes)
	}
	out := make([]byte, 0, size)
	for offset := int64(0); offset < size || size == 0; {
		length := min(int64(rangeChunk), size-offset)
		last := offset+length >= size
		raw, err := w.helperRaw(ctx, helperRequest{Op: "range", Snapshot: snapshot, Offset: offset, Length: length, Drop: last, LockDir: w.lockDir})
		if err != nil {
			return nil, err
		}
		var chunk helperResponse
		if err := json.Unmarshal(raw, &chunk); err != nil || !chunk.OK {
			return nil, errors.New("boatenv: filesystem helper returned an invalid range")
		}
		data, err := base64.StdEncoding.DecodeString(chunk.Content)
		if err != nil || int64(len(data)) != length {
			return nil, errors.New("boatenv: filesystem helper returned a short range")
		}
		out = append(out, data...)
		offset += length
		if last {
			break
		}
	}
	return out, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// classifyHelperError maps the helper's refusal codes onto the error values
// mecatl's tools and conformance tables expect.
func classifyHelperError(op, p string, err error) error {
	if err == nil {
		return nil
	}
	var semantic helperSemanticError
	if !errors.As(err, &semantic) {
		return err
	}
	switch semantic.code {
	case "not_found":
		return fmt.Errorf("boatenv: %s %q: %w", op, p, fs.ErrNotExist)
	case "exists":
		return fmt.Errorf("boatenv: %s %q: %w", op, p, fs.ErrExist)
	case "version_mismatch":
		return &tool.VersionMismatchError{Path: p}
	case "not_empty":
		return fmt.Errorf("boatenv: %s %q: %w", op, p, tool.ErrDirectoryNotEmpty)
	case "unsupported":
		return fmt.Errorf("boatenv: %s %q: %w", op, p, tool.ErrFileOperationUnsupported)
	case "escape":
		return fmt.Errorf("boatenv: %s %q: %w", op, p, ErrPathEscape)
	case "too_large":
		return fmt.Errorf("boatenv: %s %q: file exceeds the %d byte read limit", op, p, maxReadBytes)
	default:
		return fmt.Errorf("boatenv: %s %q: %w", op, p, err)
	}
}
