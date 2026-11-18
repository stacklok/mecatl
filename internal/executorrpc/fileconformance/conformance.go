// Package fileconformance checks a FileService implementation through its gRPC client.
package fileconformance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/tool"
)

// Target is a fresh service and another actor on the files it serves.
type Target struct {
	Client   executorv1.FileServiceClient
	Root     string
	External fsconformance.ExternalAccess
}

// Run runs the Workspace conformance suites against the service through Workspace.
func Run(t *testing.T, newTarget func(t *testing.T) Target) {
	fixture := func(t *testing.T) (tool.Workspace, fsconformance.ExternalAccess) {
		target := newTarget(t)
		return NewWorkspace(target.Client, target.Root), target.External
	}
	workspace := func(t *testing.T) tool.Workspace {
		ws, _ := fixture(t)
		return ws
	}
	t.Run("Workspace", func(t *testing.T) { fsconformance.Run(t, workspace) })
	t.Run("Namespace", func(t *testing.T) { fsconformance.RunNamespace(t, workspace) })
	t.Run("MutationAffinity", func(t *testing.T) { fsconformance.RunMutationAffinity(t, fixture) })
	t.Run("ExternalMutationConflict", func(t *testing.T) {
		fsconformance.RunExternalMutationConflict(t, fixture)
	})
}

// Dial serves srv on an in-memory listener and returns a client of it.
func Dial(t *testing.T, srv executorv1.FileServiceServer) executorv1.FileServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	executorv1.RegisterFileServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return executorv1.NewFileServiceClient(conn)
}

// ChunkBytes is the most content Workspace sends in one write message.
const ChunkBytes = 3 << 20

// Workspace is a minimal tool.Workspace over a FileService client.
type Workspace struct {
	client executorv1.FileServiceClient
	root   string
}

var (
	_ tool.Workspace          = (*Workspace)(nil)
	_ tool.WorkspaceNamespace = (*Workspace)(nil)
)

// NewWorkspace returns a Workspace reporting root that calls client.
func NewWorkspace(client executorv1.FileServiceClient, root string) *Workspace {
	return &Workspace{client: client, root: root}
}

// Root implements tool.Workspace.
func (w *Workspace) Root() string { return w.root }

// Read implements tool.Workspace.
func (w *Workspace) Read(ctx context.Context, path string) ([]byte, error) {
	data, _, err := w.ReadVersion(ctx, path)
	return data, err
}

// ReadVersion implements tool.Workspace.
func (w *Workspace) ReadVersion(
	ctx context.Context,
	path string,
) ([]byte, tool.FileVersion, error) {
	stream, err := w.client.ReadFile(ctx, &executorv1.ReadFileRequest{Path: path})
	if err != nil {
		return nil, tool.FileVersion{}, toolError(path, err)
	}
	var data []byte
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil, tool.FileVersion{}, errors.New("read ended without a version")
		}
		if err != nil {
			return nil, tool.FileVersion{}, toolError(path, err)
		}
		data = append(data, msg.GetData()...)
		if v := msg.GetVersion(); v != nil {
			return data, tool.NewFileVersion(string(v.GetToken())), nil
		}
	}
}

// Stat implements tool.Workspace.
func (w *Workspace) Stat(ctx context.Context, path string) (tool.FileInfo, error) {
	resp, err := w.client.StatFile(ctx, &executorv1.StatFileRequest{Path: path})
	if err != nil {
		return tool.FileInfo{}, toolError(path, err)
	}
	return infoIn(resp.GetInfo()), nil
}

// CreateFile implements tool.Workspace.
func (w *Workspace) CreateFile(
	ctx context.Context,
	path string,
	data []byte,
) (tool.FileVersion, error) {
	stream, err := w.client.CreateFile(ctx)
	if err != nil {
		return tool.FileVersion{}, toolError(path, err)
	}
	err = send(data, func(first bool, part []byte) error {
		msg := &executorv1.CreateFileRequest{Data: part}
		if first {
			msg.Context, msg.Path = &executorv1.RequestContext{}, path
		}
		return stream.Send(msg)
	})
	if err != nil {
		return tool.FileVersion{}, toolError(path, err)
	}
	resp, err := stream.CloseAndRecv()
	return versionIn(path, resp.GetVersion(), err)
}

// ReplaceFile implements tool.Workspace.
func (w *Workspace) ReplaceFile(
	ctx context.Context,
	path string,
	old tool.FileVersion,
	data []byte,
) (tool.FileVersion, error) {
	stream, err := w.client.ReplaceFile(ctx)
	if err != nil {
		return tool.FileVersion{}, toolError(path, err)
	}
	var expected *executorv1.FileVersion
	if token, err := tool.EncodeFileVersion(old); err == nil {
		expected = &executorv1.FileVersion{Token: []byte(token)}
	}
	err = send(data, func(first bool, part []byte) error {
		msg := &executorv1.ReplaceFileRequest{Data: part}
		if first {
			msg.Context, msg.Path = &executorv1.RequestContext{}, path
			msg.ExpectedVersion = expected
		}
		return stream.Send(msg)
	})
	if err != nil {
		return tool.FileVersion{}, toolError(path, err)
	}
	resp, err := stream.CloseAndRecv()
	return versionIn(path, resp.GetVersion(), err)
}

func send(data []byte, sendPart func(first bool, part []byte) error) error {
	first := true
	for first || len(data) > 0 {
		part := data[:min(len(data), ChunkBytes)]
		data = data[len(part):]
		if err := sendPart(first, part); err != nil {
			return err
		}
		first = false
	}
	return nil
}

// Glob implements tool.Workspace.
func (w *Workspace) Glob(ctx context.Context, pattern string) ([]string, error) {
	resp, err := w.client.Glob(ctx, &executorv1.GlobRequest{Pattern: pattern})
	if err != nil {
		return nil, toolError(pattern, err)
	}
	return resp.GetPaths(), nil
}

// Grep implements tool.Workspace.
func (w *Workspace) Grep(ctx context.Context, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	resp, err := w.client.Grep(ctx, &executorv1.GrepRequest{Pattern: pattern, PathGlob: pathGlob})
	if err != nil {
		return nil, toolError(pathGlob, err)
	}
	out := make([]tool.GrepMatch, len(resp.GetMatches()))
	for i, m := range resp.GetMatches() {
		out[i] = tool.GrepMatch{Path: m.GetPath(), Line: int(m.GetLine()), Text: m.GetText()}
	}
	return out, nil
}

// ReadDir implements tool.WorkspaceNamespace.
func (w *Workspace) ReadDir(ctx context.Context, path string) ([]tool.FileInfo, error) {
	resp, err := w.client.ReadDir(ctx, &executorv1.ReadDirRequest{Path: path})
	if err != nil {
		return nil, toolError(path, err)
	}
	out := make([]tool.FileInfo, len(resp.GetEntries()))
	for i, e := range resp.GetEntries() {
		out[i] = infoIn(e)
	}
	return out, nil
}

// Remove implements tool.WorkspaceNamespace.
func (w *Workspace) Remove(ctx context.Context, path string) error {
	_, err := w.client.RemoveFile(ctx, &executorv1.RemoveFileRequest{Path: path})
	return toolError(path, err)
}

// Rename implements tool.WorkspaceNamespace.
func (w *Workspace) Rename(ctx context.Context, oldPath, newPath string) error {
	_, err := w.client.RenameFile(ctx, &executorv1.RenameFileRequest{
		SourcePath:      oldPath,
		DestinationPath: newPath,
	})
	return toolError(oldPath, err)
}

// CopyFile implements tool.WorkspaceNamespace.
func (w *Workspace) CopyFile(
	ctx context.Context,
	source string,
	destination string,
) (tool.FileVersion, error) {
	resp, err := w.client.CopyFile(ctx, &executorv1.CopyFileRequest{
		SourcePath:      source,
		DestinationPath: destination,
	})
	return versionIn(destination, resp.GetVersion(), err)
}

func versionIn(path string, v *executorv1.FileVersion, err error) (tool.FileVersion, error) {
	if err != nil {
		return tool.FileVersion{}, toolError(path, err)
	}
	if v == nil {
		return tool.FileVersion{}, errors.New("response carries no version")
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

// Reason returns the ErrorReason a status error carries.
func Reason(err error) executorv1.ErrorReason {
	for _, d := range status.Convert(err).Details() {
		if detail, ok := d.(*executorv1.ErrorDetail); ok {
			return detail.GetReason()
		}
	}
	return executorv1.ErrorReason_ERROR_REASON_UNSPECIFIED
}

func toolError(path string, err error) error {
	if err == nil {
		return nil
	}
	switch Reason(err) {
	case executorv1.ErrorReason_ERROR_REASON_VERSION_MISMATCH:
		return &tool.VersionMismatchError{Path: path}
	case executorv1.ErrorReason_ERROR_REASON_NOT_FOUND:
		return fmt.Errorf("%s: %w: %w", path, fs.ErrNotExist, err)
	case executorv1.ErrorReason_ERROR_REASON_ALREADY_EXISTS:
		return fmt.Errorf("%s: %w: %w", path, fs.ErrExist, err)
	case executorv1.ErrorReason_ERROR_REASON_DIRECTORY_NOT_EMPTY:
		return fmt.Errorf("%s: %w: %w", path, tool.ErrDirectoryNotEmpty, err)
	case executorv1.ErrorReason_ERROR_REASON_UNSUPPORTED:
		return fmt.Errorf("%s: %w: %w", path, tool.ErrFileOperationUnsupported, err)
	default:
		return err
	}
}
