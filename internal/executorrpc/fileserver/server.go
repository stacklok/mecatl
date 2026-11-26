// Package fileserver serves the executor FileService over a Workspace.
package fileserver

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/tool"
)

// Workspace is the file system the service serves.
type Workspace interface {
	tool.Workspace
	tool.WorkspaceNamespace
	tool.BoundedWorkspaceReader
	tool.AuthorityResourceResolver
}

const (
	defaultMaxFileBytes = 5 << 20
	defaultMaxResults   = 10_000
	defaultMaxPathBytes = 4096
	maxChunkBytes       = 3 << 20
)

// Server implements executorv1.FileServiceServer.
type Server struct {
	executorv1.UnimplementedFileServiceServer
	ws           Workspace
	locks        pathLocks
	maxFileBytes int
	maxResults   int
	maxPathBytes int
	escapes      []error
	full         []error
}

// Option configures a Server.
type Option func(*Server) error

// WithMaxFileBytes bounds the content of a file read or written.
func WithMaxFileBytes(n int) Option {
	return positive(n, "max file bytes", func(s *Server) { s.maxFileBytes = n })
}

// WithMaxResults bounds the entries of a listing, glob, or grep.
func WithMaxResults(n int) Option {
	return positive(n, "max results", func(s *Server) { s.maxResults = n })
}

// WithMaxPathBytes bounds the length of a path or pattern.
func WithMaxPathBytes(n int) Option {
	return positive(n, "max path bytes", func(s *Server) { s.maxPathBytes = n })
}

// WithPathEscapeErrors names the workspace errors that report a path outside it.
func WithPathEscapeErrors(errs ...error) Option {
	return func(s *Server) error {
		s.escapes = append(s.escapes, errs...)
		return nil
	}
}

// WithStorageFullErrors names the workspace errors that report full storage.
func WithStorageFullErrors(errs ...error) Option {
	return func(s *Server) error {
		s.full = append(s.full, errs...)
		return nil
	}
}

func positive(n int, name string, set func(*Server)) Option {
	return func(s *Server) error {
		if n <= 0 {
			return errors.New(name + " must be positive")
		}
		set(s)
		return nil
	}
}

// New returns a server for ws.
func New(ws Workspace, opts ...Option) (*Server, error) {
	if ws == nil {
		return nil, errors.New("workspace is required")
	}
	s := &Server{
		ws:           ws,
		locks:        pathLocks{held: map[string]*pathLock{}},
		maxFileBytes: defaultMaxFileBytes,
		maxResults:   defaultMaxResults,
		maxPathBytes: defaultMaxPathBytes,
	}
	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// ReadFile streams a file and ends with the version of the bytes sent.
func (s *Server) ReadFile(
	req *executorv1.ReadFileRequest,
	stream executorv1.FileService_ReadFileServer,
) error {
	ctx := stream.Context()
	if err := s.checkPaths(req.GetPath()); err != nil {
		return err
	}
	data, v, err := s.ws.ReadVersionBounded(ctx, req.GetPath(), int64(s.maxFileBytes))
	if err != nil {
		return s.readError(ctx, req.GetPath(), err)
	}
	version, err := versionOut(v)
	if err != nil {
		return err
	}
	for len(data) > maxChunkBytes {
		msg := &executorv1.ReadFileResponse{Data: data[:maxChunkBytes]}
		if err := stream.Send(msg); err != nil {
			return err
		}
		data = data[maxChunkBytes:]
	}
	return stream.Send(&executorv1.ReadFileResponse{Data: data, Version: version})
}

func (s *Server) readError(ctx context.Context, p string, err error) error {
	st := s.status(err)
	if status.Code(st) != codes.Internal {
		return st
	}
	info, serr := s.ws.Stat(ctx, p)
	if serr == nil && !info.IsDir && info.Size > int64(s.maxFileBytes) {
		return limitExceeded("file exceeds limit")
	}
	return st
}

// StatFile describes one path.
func (s *Server) StatFile(
	ctx context.Context,
	req *executorv1.StatFileRequest,
) (*executorv1.StatFileResponse, error) {
	if err := s.checkPaths(req.GetPath()); err != nil {
		return nil, err
	}
	info, err := s.ws.Stat(ctx, req.GetPath())
	if err != nil {
		return nil, s.status(err)
	}
	return &executorv1.StatFileResponse{Info: infoOut(info)}, nil
}

// CreateFile writes a new file once the caller closes its stream.
func (s *Server) CreateFile(stream executorv1.FileService_CreateFileServer) error {
	first, err := stream.Recv()
	if err != nil {
		return firstMessage(err)
	}
	p := first.GetPath()
	if err := s.checkPaths(p); err != nil {
		return err
	}
	data, err := drain(stream.Recv, first.GetData(), s.maxFileBytes)
	if err != nil {
		return err
	}
	create := func(ctx context.Context) (tool.FileVersion, error) {
		return s.ws.CreateFile(ctx, p, data)
	}
	v, err := s.serialized(stream.Context(), p, create)
	if err != nil {
		return err
	}
	return stream.SendAndClose(&executorv1.CreateFileResponse{Version: v})
}

// ReplaceFile replaces a file at its expected version once the caller closes its stream.
func (s *Server) ReplaceFile(stream executorv1.FileService_ReplaceFileServer) error {
	first, err := stream.Recv()
	if err != nil {
		return firstMessage(err)
	}
	p := first.GetPath()
	if err := s.checkPaths(p); err != nil {
		return err
	}
	expected := versionIn(first.GetExpectedVersion())
	data, err := drain(stream.Recv, first.GetData(), s.maxFileBytes)
	if err != nil {
		return err
	}
	replace := func(ctx context.Context) (tool.FileVersion, error) {
		return s.ws.ReplaceFile(ctx, p, expected, data)
	}
	v, err := s.serialized(stream.Context(), p, replace)
	if err != nil {
		return err
	}
	return stream.SendAndClose(&executorv1.ReplaceFileResponse{Version: v})
}

// serialized runs a version check and write while holding the path's lock.
func (s *Server) serialized(
	ctx context.Context,
	p string,
	write func(context.Context) (tool.FileVersion, error),
) (*executorv1.FileVersion, error) {
	unlock, err := s.locks.lock(ctx, tool.LedgerKey(s.ws.Root(), p))
	if err != nil {
		return nil, status.FromContextError(err).Err()
	}
	defer unlock()
	v, err := write(ctx)
	if err != nil {
		return nil, s.status(err)
	}
	return versionOut(v)
}

// ReadDir lists the immediate children of a directory.
func (s *Server) ReadDir(
	ctx context.Context,
	req *executorv1.ReadDirRequest,
) (*executorv1.ReadDirResponse, error) {
	if err := s.checkPaths(req.GetPath()); err != nil {
		return nil, err
	}
	items, err := s.ws.ReadDir(ctx, req.GetPath())
	if err != nil {
		return nil, s.status(err)
	}
	if err := s.checkResults(len(items), req.GetMaxResults()); err != nil {
		return nil, err
	}
	out := make([]*executorv1.FileInfo, len(items))
	for i, item := range items {
		out[i] = infoOut(item)
	}
	return &executorv1.ReadDirResponse{Entries: out}, nil
}

// RemoveFile removes a file or empty directory.
func (s *Server) RemoveFile(
	ctx context.Context,
	req *executorv1.RemoveFileRequest,
) (*executorv1.RemoveFileResponse, error) {
	if err := s.checkPaths(req.GetPath()); err != nil {
		return nil, err
	}
	if err := s.ws.Remove(ctx, req.GetPath()); err != nil {
		return nil, s.status(err)
	}
	return &executorv1.RemoveFileResponse{}, nil
}

// RenameFile moves a path to an absent destination.
func (s *Server) RenameFile(
	ctx context.Context,
	req *executorv1.RenameFileRequest,
) (*executorv1.RenameFileResponse, error) {
	if err := s.checkPaths(req.GetSourcePath(), req.GetDestinationPath()); err != nil {
		return nil, err
	}
	if err := s.ws.Rename(ctx, req.GetSourcePath(), req.GetDestinationPath()); err != nil {
		return nil, s.status(err)
	}
	return &executorv1.RenameFileResponse{}, nil
}

// CopyFile copies a file to an absent destination.
func (s *Server) CopyFile(
	ctx context.Context,
	req *executorv1.CopyFileRequest,
) (*executorv1.CopyFileResponse, error) {
	if err := s.checkPaths(req.GetSourcePath(), req.GetDestinationPath()); err != nil {
		return nil, err
	}
	v, err := s.ws.CopyFile(ctx, req.GetSourcePath(), req.GetDestinationPath())
	if err != nil {
		return nil, s.status(err)
	}
	version, err := versionOut(v)
	if err != nil {
		return nil, err
	}
	return &executorv1.CopyFileResponse{Version: version}, nil
}

// Glob returns the paths matching a pattern.
func (s *Server) Glob(
	ctx context.Context,
	req *executorv1.GlobRequest,
) (*executorv1.GlobResponse, error) {
	if err := s.checkPaths(req.GetPattern()); err != nil {
		return nil, err
	}
	paths, err := s.ws.Glob(ctx, req.GetPattern())
	if err != nil {
		return nil, s.status(err)
	}
	if err := s.checkResults(len(paths), req.GetMaxResults()); err != nil {
		return nil, err
	}
	for i, p := range paths {
		paths[i] = validUTF8(p)
	}
	return &executorv1.GlobResponse{Paths: paths}, nil
}

// Grep returns the lines matching a regular expression.
func (s *Server) Grep(
	ctx context.Context,
	req *executorv1.GrepRequest,
) (*executorv1.GrepResponse, error) {
	if err := s.checkPaths(req.GetPattern(), req.GetPathGlob()); err != nil {
		return nil, err
	}
	matches, err := s.ws.Grep(ctx, req.GetPattern(), req.GetPathGlob())
	if err != nil {
		return nil, s.status(err)
	}
	if err := s.checkResults(len(matches), req.GetMaxResults()); err != nil {
		return nil, err
	}
	out := make([]*executorv1.GrepMatch, len(matches))
	for i, m := range matches {
		out[i] = &executorv1.GrepMatch{
			Path: validUTF8(m.Path),
			Line: int32(m.Line), //nolint:gosec // Line numbers of bounded files fit in int32.
			Text: validUTF8(m.Text),
		}
	}
	return &executorv1.GrepResponse{Matches: out}, nil
}

// ResolveAuthorityPath returns the physical target of a workspace path.
func (s *Server) ResolveAuthorityPath(
	_ context.Context,
	req *executorv1.ResolveAuthorityPathRequest,
) (*executorv1.ResolveAuthorityPathResponse, error) {
	if err := s.checkPaths(req.GetPath()); err != nil {
		return nil, err
	}
	target, workspace, err := s.ws.AuthorityResourcePath(req.GetPath())
	if err != nil {
		return nil, s.status(err)
	}
	return &executorv1.ResolveAuthorityPathResponse{Target: target, Workspace: workspace}, nil
}

func (s *Server) checkPaths(paths ...string) error {
	for _, p := range paths {
		if len(p) > s.maxPathBytes {
			return limitExceeded("path exceeds limit")
		}
	}
	return nil
}

func (s *Server) checkResults(n int, requested uint32) error {
	limit := s.maxResults
	if requested > 0 && int64(requested) < int64(limit) {
		limit = int(requested)
	}
	if n > limit {
		return limitExceeded("result exceeds limit")
	}
	return nil
}

func (s *Server) status(err error) error {
	var mismatch *tool.VersionMismatchError
	switch {
	case isAny(err, s.full):
		return status.Error(codes.ResourceExhausted, "workspace storage is full")
	case errors.As(err, &mismatch):
		return failure(codes.FailedPrecondition, reasonVersionMismatch, "file version mismatch")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	case isAny(err, s.escapes):
		return failure(codes.PermissionDenied, reasonPathEscape, "path is outside workspace")
	case errors.Is(err, fs.ErrNotExist):
		return failure(codes.NotFound, reasonNotFound, "path not found")
	case errors.Is(err, fs.ErrExist):
		return failure(codes.AlreadyExists, reasonAlreadyExists, "path already exists")
	case errors.Is(err, tool.ErrDirectoryNotEmpty):
		return failure(codes.FailedPrecondition, reasonNotEmpty, "directory is not empty")
	case errors.Is(err, tool.ErrFileOperationUnsupported):
		return failure(codes.Unimplemented, reasonUnsupported, "file operation unsupported")
	default:
		return status.Error(codes.Internal, "file operation failed")
	}
}

const (
	reasonVersionMismatch = executorv1.ErrorReason_ERROR_REASON_VERSION_MISMATCH
	reasonPathEscape      = executorv1.ErrorReason_ERROR_REASON_PATH_ESCAPE
	reasonNotFound        = executorv1.ErrorReason_ERROR_REASON_NOT_FOUND
	reasonAlreadyExists   = executorv1.ErrorReason_ERROR_REASON_ALREADY_EXISTS
	reasonNotEmpty        = executorv1.ErrorReason_ERROR_REASON_DIRECTORY_NOT_EMPTY
	reasonUnsupported     = executorv1.ErrorReason_ERROR_REASON_UNSUPPORTED
	reasonLimitExceeded   = executorv1.ErrorReason_ERROR_REASON_LIMIT_EXCEEDED
)

func isAny(err error, targets []error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

type chunk interface{ GetData() []byte }

// drain appends the data of every later message until the caller closes its stream.
func drain[T chunk](recv func() (T, error), first []byte, limit int) ([]byte, error) {
	data := append([]byte(nil), first...)
	for {
		if len(data) > limit {
			return nil, limitExceeded("file exceeds limit")
		}
		msg, err := recv()
		if errors.Is(err, io.EOF) {
			return data, nil
		}
		if err != nil {
			return nil, err
		}
		data = append(data, msg.GetData()...)
	}
}

func firstMessage(err error) error {
	if errors.Is(err, io.EOF) {
		return status.Error(codes.InvalidArgument, "first message is required")
	}
	return err
}

func versionIn(v *executorv1.FileVersion) tool.FileVersion {
	if v == nil {
		return tool.FileVersion{}
	}
	return tool.NewFileVersion(string(v.GetToken()))
}

func versionOut(v tool.FileVersion) (*executorv1.FileVersion, error) {
	token, err := tool.EncodeFileVersion(v)
	if err != nil {
		return nil, status.Error(codes.Internal, "workspace returned no version")
	}
	return &executorv1.FileVersion{Token: []byte(token)}, nil
}

func infoOut(i tool.FileInfo) *executorv1.FileInfo {
	return &executorv1.FileInfo{
		Name:    validUTF8(i.Name),
		Size:    i.Size,
		Mode:    uint32(i.Mode),
		ModTime: timestamppb.New(i.ModTime),
		IsDir:   i.IsDir,
	}
}

func validUTF8(s string) string { return strings.ToValidUTF8(s, "�") }

func limitExceeded(msg string) error {
	return failure(codes.ResourceExhausted, reasonLimitExceeded, msg)
}

func failure(code codes.Code, reason executorv1.ErrorReason, msg string) error {
	st, err := status.New(code, msg).WithDetails(&executorv1.ErrorDetail{Reason: reason})
	if err != nil {
		return status.Error(code, msg)
	}
	return st.Err()
}
