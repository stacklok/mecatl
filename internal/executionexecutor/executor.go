// Package executionexecutor is the fixed, credential-free workload helper.
package executionexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/executionenv"
)

// Limits bounds one executor operation.
type Limits struct {
	MaxFileBytes   int
	MaxEntries     int
	CommandTimeout time.Duration
}

// Executor serves one credential-free workload-helper process.
type Executor struct {
	workspace *osfs.Workspace
	limits    Limits
}

// New constructs an executor confined to root.
func New(root string, limits Limits) (*Executor, error) {
	if limits.MaxFileBytes <= 0 || limits.MaxFileBytes > executionenv.MaxFileBytes {
		limits.MaxFileBytes = executionenv.MaxFileBytes
	}
	if limits.MaxEntries <= 0 || limits.MaxEntries > executionenv.MaxListEntries {
		limits.MaxEntries = executionenv.MaxListEntries
	}
	if limits.CommandTimeout <= 0 {
		limits.CommandTimeout = 30 * time.Second
	}
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		return nil, err
	}
	return &Executor{workspace: ws, limits: limits}, nil
}

// Close releases executor resources.
func (*Executor) Close() error { return nil }

// Execute performs one bounded executor operation.
func (e *Executor) Execute(ctx context.Context, q executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) { //nolint:gocyclo // The operation switch keeps validation and dispatch in one boundary.
	if !q.Operation.Valid() {
		return executionenv.ExecutorResponse{}, bad("unsupported executor operation")
	}
	if len(q.Data) > e.limits.MaxFileBytes || len(q.Path) > executionenv.MaxPathBytes || len(q.Destination) > executionenv.MaxPathBytes || len(q.Pattern) > executionenv.MaxPathBytes || q.Limit < 0 || q.Limit > executionenv.MaxListEntries {
		return executionenv.ExecutorResponse{}, coded(executionenv.CodeResourceExhausted, "executor request exceeds configured bounds")
	}
	switch q.Operation {
	case executionenv.OpFileRead:
		return e.read(ctx, q)
	case executionenv.OpFileResolveAuthority:
		target, workspace, err := e.workspace.AuthorityResourcePath(q.Path)
		if err != nil {
			return executionenv.ExecutorResponse{}, mapErr(err)
		}
		return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{AuthorityTarget: target, AuthorityWorkspace: workspace}}, nil
	case executionenv.OpFileStat:
		return e.stat(ctx, q)
	case executionenv.OpFileCreate:
		return e.create(ctx, q)
	case executionenv.OpFileReplace:
		return e.replace(ctx, q)
	case executionenv.OpFileList:
		return e.list(ctx, q)
	case executionenv.OpFileRemove:
		return executionenv.ExecutorResponse{}, mapErr(e.workspace.Remove(ctx, q.Path))
	case executionenv.OpFileRename:
		return executionenv.ExecutorResponse{}, mapErr(e.workspace.Rename(ctx, q.Path, q.Destination))
	case executionenv.OpFileCopy:
		v, err := e.workspace.CopyFile(ctx, q.Path, q.Destination)
		if err != nil {
			return executionenv.ExecutorResponse{}, mapErr(err)
		}
		s, _ := tool.EncodeFileVersion(v)
		return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{Version: s}}, nil
	case executionenv.OpFileGlob:
		paths, err := e.workspace.Glob(ctx, q.Pattern)
		if err != nil {
			return executionenv.ExecutorResponse{}, mapErr(err)
		}
		if len(paths) > e.resultLimit(q.Limit) {
			return executionenv.ExecutorResponse{}, coded(executionenv.CodeResourceExhausted, "glob result exceeds configured limit")
		}
		return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{Paths: paths}}, nil
	case executionenv.OpFileGrep:
		matches, err := e.workspace.Grep(ctx, q.Pattern, q.Path)
		if err != nil {
			return executionenv.ExecutorResponse{}, mapErr(err)
		}
		if len(matches) > e.resultLimit(q.Limit) {
			return executionenv.ExecutorResponse{}, coded(executionenv.CodeResourceExhausted, "grep result exceeds configured limit")
		}
		out := make([]executionenv.GrepMatch, len(matches))
		for i, m := range matches {
			out[i] = executionenv.GrepMatch{Path: m.Path, Line: m.Line, Text: strings.ToValidUTF8(m.Text, "�")}
		}
		return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{Matches: out}}, nil
	case executionenv.OpCommandStart:
		return e.command(ctx, q)
	default:
		return executionenv.ExecutorResponse{}, bad("operation is handled by provider, not workload")
	}
}
func (e *Executor) read(ctx context.Context, q executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) {
	b, v, err := e.workspace.ReadVersion(ctx, q.Path)
	if err != nil {
		return executionenv.ExecutorResponse{}, mapErr(err)
	}
	if len(b) > e.limits.MaxFileBytes {
		return executionenv.ExecutorResponse{}, coded(executionenv.CodeResourceExhausted, "file exceeds configured limit")
	}
	s, _ := tool.EncodeFileVersion(v)
	return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{Data: b, Version: s}}, nil
}
func (e *Executor) stat(ctx context.Context, q executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) {
	i, err := e.workspace.Stat(ctx, q.Path)
	if err != nil {
		return executionenv.ExecutorResponse{}, mapErr(err)
	}
	x := convertInfo(i)
	return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{Info: &x}}, nil
}
func (e *Executor) create(ctx context.Context, q executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) {
	v, err := e.workspace.CreateFile(ctx, q.Path, q.Data)
	if err != nil {
		return executionenv.ExecutorResponse{}, mapErr(err)
	}
	s, _ := tool.EncodeFileVersion(v)
	return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{Version: s}}, nil
}
func (e *Executor) replace(ctx context.Context, q executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) {
	v, err := e.workspace.ReplaceFile(ctx, q.Path, tool.DecodeFileVersion(q.Version), q.Data)
	if err != nil {
		return executionenv.ExecutorResponse{}, mapErr(err)
	}
	s, _ := tool.EncodeFileVersion(v)
	return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{Version: s}}, nil
}
func (e *Executor) list(ctx context.Context, q executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) {
	items, err := e.workspace.ReadDir(ctx, q.Path)
	if err != nil {
		return executionenv.ExecutorResponse{}, mapErr(err)
	}
	if len(items) > e.resultLimit(q.Limit) {
		return executionenv.ExecutorResponse{}, coded(executionenv.CodeResourceExhausted, "directory exceeds configured limit")
	}
	out := make([]executionenv.FileInfo, len(items))
	for i, v := range items {
		out[i] = convertInfo(v)
	}
	return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{Entries: out}}, nil
}
func (e *Executor) command(ctx context.Context, q executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) {
	if q.CommandID == "" || len(q.Command) == 0 || len(q.Command) > executionenv.MaxCommandBytes {
		return executionenv.ExecutorResponse{}, bad("command id and bounded command are required")
	}
	d := e.limits.CommandTimeout
	if q.TimeoutMillis > 0 {
		requested := time.Duration(q.TimeoutMillis) * time.Millisecond
		if requested < d {
			d = requested
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	result, cleanupProven, err := runIsolatedCommand(runCtx, e.workspace.Root(), q.Command, e.limits.MaxFileBytes)
	stdout, stderr, truncated := capStreams(result.stdout, result.stderr, e.limits.MaxFileBytes)
	truncated = truncated || result.truncated
	if !cleanupProven {
		return executionenv.ExecutorResponse{Command: &executionenv.CommandStatusResponse{CommandID: q.CommandID, State: executionenv.CommandFenceUnknown, Stdout: stdout, Stderr: stderr, Truncated: truncated}}, nil
	}
	state := executionenv.CommandSucceeded
	if errors.Is(runCtx.Err(), context.Canceled) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		state = executionenv.CommandCancelled
	} else if err != nil || result.exitCode != 0 {
		state = executionenv.CommandFailed
	}
	receiptInput := append(append([]byte(q.CommandID+"\x00"), stdout...), 0)
	receiptHash := sha256.Sum256(append(receiptInput, stderr...))
	return executionenv.ExecutorResponse{Command: &executionenv.CommandStatusResponse{CommandID: q.CommandID, State: state, ExitCode: result.exitCode, Stdout: stdout, Stderr: stderr, Truncated: truncated, TerminalReceipt: hex.EncodeToString(receiptHash[:])}}, nil
}
func convertInfo(i tool.FileInfo) executionenv.FileInfo {
	return executionenv.FileInfo{Name: i.Name, Size: i.Size, Mode: uint32(i.Mode), ModTime: i.ModTime, IsDir: i.IsDir}
}
func (e *Executor) resultLimit(requested int) int {
	if requested > 0 && requested < e.limits.MaxEntries {
		return requested
	}
	return e.limits.MaxEntries
}
func capStreams(stdout, stderr []byte, n int) ([]byte, []byte, bool) {
	original := len(stdout) + len(stderr)
	stdout = capOutput(stdout, n)
	remaining := n - len(stdout)
	if remaining < 0 {
		remaining = 0
	}
	stderr = capOutput(stderr, remaining)
	return stdout, stderr, original > len(stdout)+len(stderr)
}

func capOutput(b []byte, n int) []byte {
	b = []byte(strings.ToValidUTF8(string(b), "�"))
	if len(b) > n {
		b = b[:n]
		for !utf8.Valid(b) {
			b = b[:len(b)-1]
		}
	}
	return b
}
func bad(msg string) error { return coded(executionenv.CodeInvalidArgument, msg) }
func coded(code executionenv.ErrorCode, msg string) error {
	return &executionenv.Error{Code: code, Message: msg}
}
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var mismatch *tool.VersionMismatchError
	switch {
	case errors.As(err, &mismatch):
		return coded(executionenv.CodeVersionMismatch, "file version mismatch")
	case errors.Is(err, fs.ErrNotExist):
		return coded(executionenv.CodeNotFound, "path not found")
	case errors.Is(err, fs.ErrExist):
		return coded(executionenv.CodeAlreadyExists, "destination already exists")
	case errors.Is(err, tool.ErrDirectoryNotEmpty):
		return coded(executionenv.CodeConflict, "directory is not empty")
	case errors.Is(err, osfs.ErrPathEscape):
		return coded(executionenv.CodePermissionDenied, "path is outside workspace")
	default:
		return err
	}
}
