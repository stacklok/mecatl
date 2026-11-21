package executioncontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/executionenv"
	"github.com/stacklok/mecatl/internal/executorrpc/executorclient"
)

const executorWorkspaceRoot = "/workspace"

// ServerTransport performs ExecutionProviderService file and command requests
// through the executor's gRPC server.
type ServerTransport struct {
	connect ExecutorConnectFunc
}

// NewServerTransport connects to executors with connect.
func NewServerTransport(connect ExecutorConnectFunc) *ServerTransport {
	return &ServerTransport{connect: connect}
}

// Execute implements ExecutorTransport.
func (t *ServerTransport) Execute(
	ctx context.Context,
	route ExecutorRoute,
	req executionenv.ExecutorRequest,
) (executionenv.ExecutorResponse, error) {
	if !req.Operation.Valid() {
		return executionenv.ExecutorResponse{}, coded(
			executionenv.CodeInvalidArgument,
			"unsupported executor operation",
		)
	}
	conn, release, err := t.connect(route.Pin)
	if err != nil {
		return executionenv.ExecutorResponse{}, &executionenv.Error{
			Code:      executionenv.CodeNotReady,
			Message:   "executor is unreachable",
			Retryable: true,
		}
	}
	defer release()
	if req.Operation == executionenv.OpCommandStart {
		return runCommand(ctx, conn, route, req)
	}
	ws := executorclient.NewWorkspace(
		conn,
		executorWorkspaceRoot,
		nil,
		executorclient.WithMaxFileBytes(int(route.MaxFileBytes)),
	)
	out, err := fileOperation(ctx, ws, req)
	if err != nil {
		return executionenv.ExecutorResponse{}, transportError(err)
	}
	return executionenv.ExecutorResponse{FileResponse: out}, nil
}

//nolint:gocyclo // One case per file operation.
func fileOperation(
	ctx context.Context,
	ws *executorclient.Workspace,
	q executionenv.ExecutorRequest,
) (executionenv.FileResponse, error) {
	switch q.Operation {
	case executionenv.OpFileRead:
		data, v, err := ws.ReadVersion(ctx, q.Path)
		return executionenv.FileResponse{Data: data, Version: encodeVersion(v)}, err
	case executionenv.OpFileResolveAuthority:
		target, workspace, err := ws.AuthorityResourcePath(q.Path)
		out := executionenv.FileResponse{AuthorityTarget: target, AuthorityWorkspace: workspace}
		return out, err
	case executionenv.OpFileStat:
		info, err := ws.Stat(ctx, q.Path)
		out := fileInfo(info)
		return executionenv.FileResponse{Info: &out}, err
	case executionenv.OpFileCreate:
		v, err := ws.CreateFile(ctx, q.Path, q.Data)
		return executionenv.FileResponse{Version: encodeVersion(v)}, err
	case executionenv.OpFileReplace:
		v, err := ws.ReplaceFile(ctx, q.Path, tool.DecodeFileVersion(q.Version), q.Data)
		return executionenv.FileResponse{Version: encodeVersion(v)}, err
	case executionenv.OpFileCopy:
		v, err := ws.CopyFile(ctx, q.Path, q.Destination)
		return executionenv.FileResponse{Version: encodeVersion(v)}, err
	case executionenv.OpFileRemove:
		return executionenv.FileResponse{}, ws.Remove(ctx, q.Path)
	case executionenv.OpFileRename:
		return executionenv.FileResponse{}, ws.Rename(ctx, q.Path, q.Destination)
	case executionenv.OpFileList:
		items, err := ws.ReadDir(ctx, q.Path)
		if err != nil {
			return executionenv.FileResponse{}, err
		}
		if len(items) > resultLimit(q.Limit) {
			return executionenv.FileResponse{}, errResultLimit
		}
		out := make([]executionenv.FileInfo, len(items))
		for i, item := range items {
			out[i] = fileInfo(item)
		}
		return executionenv.FileResponse{Entries: out}, nil
	case executionenv.OpFileGlob:
		paths, err := ws.Glob(ctx, q.Pattern)
		if err != nil {
			return executionenv.FileResponse{}, err
		}
		if len(paths) > resultLimit(q.Limit) {
			return executionenv.FileResponse{}, errResultLimit
		}
		return executionenv.FileResponse{Paths: paths}, nil
	case executionenv.OpFileGrep:
		matches, err := ws.Grep(ctx, q.Pattern, q.Path)
		if err != nil {
			return executionenv.FileResponse{}, err
		}
		if len(matches) > resultLimit(q.Limit) {
			return executionenv.FileResponse{}, errResultLimit
		}
		out := make([]executionenv.GrepMatch, len(matches))
		for i, m := range matches {
			out[i] = executionenv.GrepMatch{
				Path: m.Path,
				Line: m.Line,
				Text: strings.ToValidUTF8(m.Text, "�"),
			}
		}
		return executionenv.FileResponse{Matches: out}, nil
	default:
		return executionenv.FileResponse{}, coded(
			executionenv.CodeInvalidArgument,
			"operation is handled by provider, not workload",
		)
	}
}

func runCommand(
	ctx context.Context,
	conn grpc.ClientConnInterface,
	route ExecutorRoute,
	q executionenv.ExecutorRequest,
) (executionenv.ExecutorResponse, error) {
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(q.TimeoutMillis)*time.Millisecond)
	defer cancel()
	runner := executorclient.NewRunner(
		conn,
		executorWorkspaceRoot,
		nil,
		executorclient.WithMaxOutputBytes(int(route.MaxCommandBytes)),
	)
	out, err := runner.Run(runCtx, q.Command)
	state, err := commandState(out, err)
	if err != nil {
		return executionenv.ExecutorResponse{}, err
	}
	stdout, stderr := []byte(out.Stdout), []byte(out.Stderr)
	return executionenv.ExecutorResponse{Command: &executionenv.CommandStatusResponse{
		CommandID:       q.CommandID,
		State:           state,
		ExitCode:        out.ExitCode,
		Stdout:          stdout,
		Stderr:          stderr,
		TerminalReceipt: terminalReceipt(q.CommandID, stdout, stderr),
	}}, nil
}

// commandState accepts only a result the executor confirmed; the old API has
// no way to report output alongside an unknown outcome.
func commandState(out tool.CommandResult, err error) (executionenv.CommandState, error) {
	switch {
	case err == nil && out.ExitCode == 0:
		return executionenv.CommandSucceeded, nil
	case err == nil:
		return executionenv.CommandFailed, nil
	case status.Code(err) == codes.Unimplemented:
		return "", transportError(err)
	case errors.Is(err, executorclient.ErrOutcomeUnknown):
		return "", coded(executionenv.CodeFenceUnknown, "command outcome is unknown")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return executionenv.CommandCancelled, nil
	case errors.Is(err, tool.ErrNoShell):
		return "", coded(executionenv.CodePermissionDenied, "executor has no shell")
	default:
		return "", transportError(err)
	}
}

// terminalReceipt keeps the old response field populated until #2718 removes it.
func terminalReceipt(id string, stdout, stderr []byte) string {
	sum := sha256.New()
	for _, part := range [][]byte{[]byte(id), stdout, stderr} {
		_, _ = sum.Write(part)
		_, _ = sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

var errResultLimit = coded(executionenv.CodeResourceExhausted, "result exceeds configured limit")

func resultLimit(requested int) int {
	if requested > 0 && requested < executionenv.MaxListEntries {
		return requested
	}
	return executionenv.MaxListEntries
}

func transportError(err error) error {
	var mismatch *tool.VersionMismatchError
	var controlled *executionenv.Error
	switch {
	case errors.As(err, &controlled):
		return controlled
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.As(err, &mismatch):
		return coded(executionenv.CodeVersionMismatch, "file version mismatch")
	case errors.Is(err, fs.ErrNotExist):
		return coded(executionenv.CodeNotFound, "path not found")
	case errors.Is(err, fs.ErrExist):
		return coded(executionenv.CodeAlreadyExists, "destination already exists")
	case errors.Is(err, tool.ErrDirectoryNotEmpty):
		return coded(executionenv.CodeDirectoryNotEmpty, "directory is not empty")
	case errors.Is(err, executorclient.ErrLimitExceeded):
		return coded(executionenv.CodeResourceExhausted, "executor response exceeds profile bound")
	case errors.Is(err, tool.ErrFileOperationUnsupported):
		return coded(executionenv.CodeInvalidArgument, "unsupported executor operation")
	}
	switch status.Code(err) {
	case codes.PermissionDenied:
		return coded(executionenv.CodePermissionDenied, "path is outside workspace")
	case codes.InvalidArgument:
		return coded(executionenv.CodeInvalidArgument, "executor rejected the request")
	case codes.ResourceExhausted:
		return coded(
			executionenv.CodeResourceExhausted,
			"executor request exceeds configured bounds",
		)
	case codes.Unimplemented:
		return coded(executionenv.CodeNotReady, "executor does not serve this operation")
	case codes.Unavailable:
		return &executionenv.Error{
			Code:      executionenv.CodeNotReady,
			Message:   "executor is unavailable",
			Retryable: true,
		}
	default:
		return err
	}
}

func coded(code executionenv.ErrorCode, message string) *executionenv.Error {
	return &executionenv.Error{Code: code, Message: message}
}

func encodeVersion(v tool.FileVersion) string {
	s, _ := tool.EncodeFileVersion(v)
	return s
}

func fileInfo(i tool.FileInfo) executionenv.FileInfo {
	return executionenv.FileInfo{
		Name:    i.Name,
		Size:    i.Size,
		Mode:    uint32(i.Mode),
		ModTime: i.ModTime,
		IsDir:   i.IsDir,
	}
}

var _ ExecutorTransport = (*ServerTransport)(nil)
