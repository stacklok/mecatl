// Package executorclient implements the engine's Workspace and CommandRunner over
// the executor FileService and CommandService, against the trusted service or an
// executor. Semantics: docs/drafts/kubernetes-execution/executor-protocol.md.
package executorclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/tool"
)

// RequestContextFunc returns the request context sent with one call. A nil
// func sends an empty context, which an executor ignores.
type RequestContextFunc func(context.Context) (*executorv1.RequestContext, error)

func (f RequestContextFunc) get(ctx context.Context) (*executorv1.RequestContext, error) {
	if f == nil {
		return &executorv1.RequestContext{}, nil
	}
	rc, err := f(ctx)
	if err != nil {
		return nil, err
	}
	if rc == nil {
		return nil, errors.New("executor: request context unavailable")
	}
	return rc, nil
}

// ChunkBytes is the most file content sent in one write message.
const ChunkBytes = 3 << 20

const authorityResolveLimit = 10 * time.Second

var errProtocol = errors.New("executor: protocol violation")

// Workspace is a tool.Workspace served by a FileService. It sends every call
// once; conn must not enable gRPC retries.
type Workspace struct {
	files   executorv1.FileServiceClient
	root    string
	context RequestContextFunc
	limits  limits
}

var (
	_ tool.Workspace                 = (*Workspace)(nil)
	_ tool.WorkspaceNamespace        = (*Workspace)(nil)
	_ tool.AuthorityResourceResolver = (*Workspace)(nil)
)

// NewWorkspace returns a Workspace rooted at root, the server's workspace root.
func NewWorkspace(
	conn grpc.ClientConnInterface,
	root string,
	rc RequestContextFunc,
	opts ...Option,
) *Workspace {
	return &Workspace{
		files:   executorv1.NewFileServiceClient(conn),
		root:    root,
		context: rc,
		limits:  newLimits(opts),
	}
}

// Root implements tool.WorkspaceReader.
func (w *Workspace) Root() string { return w.root }

// Read implements tool.WorkspaceReader.
func (w *Workspace) Read(ctx context.Context, path string) ([]byte, error) {
	data, _, err := w.ReadVersion(ctx, path)
	return data, err
}

// ReadVersion implements tool.Workspace.
func (w *Workspace) ReadVersion(
	ctx context.Context,
	path string,
) ([]byte, tool.FileVersion, error) {
	rc, err := w.context.get(ctx)
	if err != nil {
		return nil, tool.FileVersion{}, err
	}
	ctx, cancel := w.bound(ctx)
	defer cancel()
	stream, err := w.files.ReadFile(ctx, &executorv1.ReadFileRequest{Context: rc, Path: path})
	if err != nil {
		return nil, tool.FileVersion{}, fileError(ctx, path, err)
	}
	var data []byte
	var version *executorv1.FileVersion
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, tool.FileVersion{}, fileError(ctx, path, err)
		}
		if version != nil {
			return nil, tool.FileVersion{}, fmt.Errorf("%w: data after version", errProtocol)
		}
		if len(data)+len(msg.GetData()) > w.limits.maxFileBytes {
			return nil, tool.FileVersion{}, fmt.Errorf("%s: %w", path, ErrLimitExceeded)
		}
		data = append(data, msg.GetData()...)
		version = msg.GetVersion()
	}
	if version == nil {
		return nil, tool.FileVersion{}, fmt.Errorf("%w: read ended without a version", errProtocol)
	}
	return data, tool.NewFileVersion(string(version.GetToken())), nil
}

// Stat implements tool.WorkspaceReader.
func (w *Workspace) Stat(ctx context.Context, path string) (tool.FileInfo, error) {
	rc, err := w.context.get(ctx)
	if err != nil {
		return tool.FileInfo{}, err
	}
	ctx, cancel := w.bound(ctx)
	defer cancel()
	resp, err := w.files.StatFile(ctx, &executorv1.StatFileRequest{Context: rc, Path: path})
	if err != nil {
		return tool.FileInfo{}, fileError(ctx, path, err)
	}
	if resp.GetInfo() == nil {
		return tool.FileInfo{}, fmt.Errorf("%w: stat without metadata", errProtocol)
	}
	return infoIn(resp.GetInfo()), nil
}

// CreateFile implements tool.Workspace.
func (w *Workspace) CreateFile(
	ctx context.Context,
	path string,
	data []byte,
) (tool.FileVersion, error) {
	rc, err := w.context.get(ctx)
	if err != nil {
		return tool.FileVersion{}, err
	}
	ctx, cancel := w.bound(ctx)
	defer cancel()
	stream, err := w.files.CreateFile(ctx)
	if err != nil {
		return tool.FileVersion{}, fileError(ctx, path, err)
	}
	err = sendChunks(data, func(first bool, part []byte) error {
		msg := &executorv1.CreateFileRequest{Data: part}
		if first {
			msg.Context, msg.Path = rc, path
		}
		return stream.Send(msg)
	})
	if err != nil {
		return tool.FileVersion{}, fileError(ctx, path, err)
	}
	resp, err := stream.CloseAndRecv()
	return versionIn(ctx, path, resp.GetVersion(), err)
}

// ReplaceFile implements tool.Workspace.
func (w *Workspace) ReplaceFile(
	ctx context.Context,
	path string,
	old tool.FileVersion,
	data []byte,
) (tool.FileVersion, error) {
	rc, err := w.context.get(ctx)
	if err != nil {
		return tool.FileVersion{}, err
	}
	var expected *executorv1.FileVersion
	if token, err := tool.EncodeFileVersion(old); err == nil {
		expected = &executorv1.FileVersion{Token: []byte(token)}
	}
	ctx, cancel := w.bound(ctx)
	defer cancel()
	stream, err := w.files.ReplaceFile(ctx)
	if err != nil {
		return tool.FileVersion{}, fileError(ctx, path, err)
	}
	err = sendChunks(data, func(first bool, part []byte) error {
		msg := &executorv1.ReplaceFileRequest{Data: part}
		if first {
			msg.Context, msg.Path, msg.ExpectedVersion = rc, path, expected
		}
		return stream.Send(msg)
	})
	if err != nil {
		return tool.FileVersion{}, fileError(ctx, path, err)
	}
	resp, err := stream.CloseAndRecv()
	return versionIn(ctx, path, resp.GetVersion(), err)
}

// sendChunks sends data in parts of at most ChunkBytes, and empty data as one part.
// io.EOF from a send defers to the stream's status, which the close reports.
func sendChunks(data []byte, send func(first bool, part []byte) error) error {
	for first := true; first || len(data) > 0; first = false {
		part := data[:min(len(data), ChunkBytes)]
		data = data[len(part):]
		if err := send(first, part); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
	return nil
}

// Glob implements tool.Workspace.
func (w *Workspace) Glob(ctx context.Context, pattern string) ([]string, error) {
	rc, err := w.context.get(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := w.bound(ctx)
	defer cancel()
	resp, err := w.files.Glob(ctx, &executorv1.GlobRequest{Context: rc, Pattern: pattern})
	if err != nil {
		return nil, fileError(ctx, pattern, err)
	}
	return resp.GetPaths(), nil
}

// Grep implements tool.Workspace.
func (w *Workspace) Grep(ctx context.Context, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	rc, err := w.context.get(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := w.bound(ctx)
	defer cancel()
	resp, err := w.files.Grep(ctx, &executorv1.GrepRequest{
		Context:  rc,
		Pattern:  pattern,
		PathGlob: pathGlob,
	})
	if err != nil {
		return nil, fileError(ctx, pathGlob, err)
	}
	out := make([]tool.GrepMatch, len(resp.GetMatches()))
	for i, m := range resp.GetMatches() {
		out[i] = tool.GrepMatch{Path: m.GetPath(), Line: int(m.GetLine()), Text: m.GetText()}
	}
	return out, nil
}

// ReadDir implements tool.WorkspaceNamespace.
func (w *Workspace) ReadDir(ctx context.Context, path string) ([]tool.FileInfo, error) {
	rc, err := w.context.get(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := w.bound(ctx)
	defer cancel()
	resp, err := w.files.ReadDir(ctx, &executorv1.ReadDirRequest{Context: rc, Path: path})
	if err != nil {
		return nil, fileError(ctx, path, err)
	}
	out := make([]tool.FileInfo, len(resp.GetEntries()))
	for i, e := range resp.GetEntries() {
		out[i] = infoIn(e)
	}
	return out, nil
}

// Remove implements tool.WorkspaceNamespace.
func (w *Workspace) Remove(ctx context.Context, path string) error {
	rc, err := w.context.get(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := w.bound(ctx)
	defer cancel()
	_, err = w.files.RemoveFile(ctx, &executorv1.RemoveFileRequest{Context: rc, Path: path})
	return fileError(ctx, path, err)
}

// Rename implements tool.WorkspaceNamespace.
func (w *Workspace) Rename(ctx context.Context, oldPath, newPath string) error {
	rc, err := w.context.get(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := w.bound(ctx)
	defer cancel()
	_, err = w.files.RenameFile(ctx, &executorv1.RenameFileRequest{
		Context:         rc,
		SourcePath:      oldPath,
		DestinationPath: newPath,
	})
	return fileError(ctx, oldPath, err)
}

// CopyFile implements tool.WorkspaceNamespace.
func (w *Workspace) CopyFile(
	ctx context.Context,
	source string,
	destination string,
) (tool.FileVersion, error) {
	rc, err := w.context.get(ctx)
	if err != nil {
		return tool.FileVersion{}, err
	}
	ctx, cancel := w.bound(ctx)
	defer cancel()
	resp, err := w.files.CopyFile(ctx, &executorv1.CopyFileRequest{
		Context:         rc,
		SourcePath:      source,
		DestinationPath: destination,
	})
	return versionIn(ctx, destination, resp.GetVersion(), err)
}

// AuthorityResourcePath implements tool.AuthorityResourceResolver.
func (w *Workspace) AuthorityResourcePath(path string) (target, workspace string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), authorityResolveLimit)
	defer cancel()
	rc, err := w.context.get(ctx)
	if err != nil {
		return "", "", err
	}
	ctx, stop := w.bound(ctx)
	defer stop()
	resp, err := w.files.ResolveAuthorityPath(ctx, &executorv1.ResolveAuthorityPathRequest{
		Context: rc,
		Path:    path,
	})
	if err != nil {
		return "", "", fileError(ctx, path, err)
	}
	target, workspace = resp.GetTarget(), resp.GetWorkspace()
	if workspace != w.root || (target != w.root && !strings.HasPrefix(target, w.root+"/")) {
		return "", "", fmt.Errorf("%w: authority path outside workspace", errProtocol)
	}
	return target, workspace, nil
}

func versionIn(
	ctx context.Context,
	path string,
	v *executorv1.FileVersion,
	err error,
) (tool.FileVersion, error) {
	if err != nil {
		return tool.FileVersion{}, fileError(ctx, path, err)
	}
	if v == nil {
		return tool.FileVersion{}, fmt.Errorf("%w: response without a version", errProtocol)
	}
	return tool.NewFileVersion(string(v.GetToken())), nil
}

func infoIn(i *executorv1.FileInfo) tool.FileInfo {
	return tool.FileInfo{
		Name:    i.GetName(),
		Size:    i.GetSize(),
		Mode:    fs.FileMode(i.GetMode()),
		ModTime: i.GetModTime().AsTime(),
		IsDir:   i.GetIsDir(),
	}
}

func reason(err error) executorv1.ErrorReason {
	for _, d := range status.Convert(err).Details() {
		if detail, ok := d.(*executorv1.ErrorDetail); ok {
			return detail.GetReason()
		}
	}
	return executorv1.ErrorReason_ERROR_REASON_UNSPECIFIED
}

func fileError(ctx context.Context, path string, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := callContextError(ctx, path, err); ctxErr != nil {
		return ctxErr
	}
	switch reason(err) {
	case executorv1.ErrorReason_ERROR_REASON_VERSION_MISMATCH:
		return &tool.VersionMismatchError{Path: path}
	case executorv1.ErrorReason_ERROR_REASON_NOT_FOUND:
		return fmt.Errorf("%s: %w", path, fs.ErrNotExist)
	case executorv1.ErrorReason_ERROR_REASON_ALREADY_EXISTS:
		return fmt.Errorf("%s: %w", path, fs.ErrExist)
	case executorv1.ErrorReason_ERROR_REASON_DIRECTORY_NOT_EMPTY:
		return fmt.Errorf("%s: %w", path, tool.ErrDirectoryNotEmpty)
	case executorv1.ErrorReason_ERROR_REASON_UNSUPPORTED:
		return fmt.Errorf("%s: %w", path, tool.ErrFileOperationUnsupported)
	default:
		return fmt.Errorf("%s: %w", path, err)
	}
}

// callContextError returns the error that ended the call through ctx, if one did.
// gRPC can report a passed deadline before ctx's own timer fires.
func callContextError(ctx context.Context, path string, err error) error {
	code := status.Code(err)
	if code != codes.Canceled && code != codes.DeadlineExceeded {
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		<-ctx.Done()
	}
	if ctx.Err() == nil {
		return nil
	}
	if cause := context.Cause(ctx); errors.Is(cause, ErrStorageTimeout) {
		return fmt.Errorf("%s: %w", path, cause)
	}
	return ctx.Err()
}

func (w *Workspace) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(ctx, w.limits.fileCallTimeout, ErrStorageTimeout)
}
