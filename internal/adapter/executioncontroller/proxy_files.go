package executioncontroller

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

// FileProxy is the provider's authorizing FileService: it checks each call and
// forwards it unchanged to the session's executor.
type FileProxy struct {
	executorv1.UnimplementedFileServiceServer
	proxy executorProxy
}

// NewFileProxy authorizes with h's client policy and backend.
func NewFileProxy(h *Handler, connector ExecutorConnectFunc) *FileProxy {
	return &FileProxy{proxy: executorProxy{
		handler:    h,
		connector:  connector,
		capability: executionenv.CapabilityFilesystem,
	}}
}

type contextual interface {
	GetContext() *executorv1.RequestContext
}

func forwardUnary[Req contextual, Resp any](
	ctx context.Context,
	p *FileProxy,
	req Req,
	call func(executorv1.FileServiceClient, context.Context, Req, ...grpc.CallOption) (Resp, error),
) (Resp, error) {
	var zero Resp
	a, err := p.proxy.open(ctx, req.GetContext())
	if err != nil {
		return zero, err
	}
	defer a.release()
	resp, err := call(executorv1.NewFileServiceClient(a.conn), ctx, req)
	if err != nil {
		return zero, executorError(err)
	}
	return resp, nil
}

// StatFile implements executorv1.FileServiceServer.
func (p *FileProxy) StatFile(
	ctx context.Context,
	req *executorv1.StatFileRequest,
) (*executorv1.StatFileResponse, error) {
	return forwardUnary(ctx, p, req, executorv1.FileServiceClient.StatFile)
}

// ReadDir implements executorv1.FileServiceServer.
func (p *FileProxy) ReadDir(
	ctx context.Context,
	req *executorv1.ReadDirRequest,
) (*executorv1.ReadDirResponse, error) {
	return forwardUnary(ctx, p, req, executorv1.FileServiceClient.ReadDir)
}

// RemoveFile implements executorv1.FileServiceServer.
func (p *FileProxy) RemoveFile(
	ctx context.Context,
	req *executorv1.RemoveFileRequest,
) (*executorv1.RemoveFileResponse, error) {
	return forwardUnary(ctx, p, req, executorv1.FileServiceClient.RemoveFile)
}

// RenameFile implements executorv1.FileServiceServer.
func (p *FileProxy) RenameFile(
	ctx context.Context,
	req *executorv1.RenameFileRequest,
) (*executorv1.RenameFileResponse, error) {
	return forwardUnary(ctx, p, req, executorv1.FileServiceClient.RenameFile)
}

// CopyFile implements executorv1.FileServiceServer.
func (p *FileProxy) CopyFile(
	ctx context.Context,
	req *executorv1.CopyFileRequest,
) (*executorv1.CopyFileResponse, error) {
	return forwardUnary(ctx, p, req, executorv1.FileServiceClient.CopyFile)
}

// Glob implements executorv1.FileServiceServer.
func (p *FileProxy) Glob(
	ctx context.Context,
	req *executorv1.GlobRequest,
) (*executorv1.GlobResponse, error) {
	return forwardUnary(ctx, p, req, executorv1.FileServiceClient.Glob)
}

// Grep implements executorv1.FileServiceServer.
func (p *FileProxy) Grep(
	ctx context.Context,
	req *executorv1.GrepRequest,
) (*executorv1.GrepResponse, error) {
	return forwardUnary(ctx, p, req, executorv1.FileServiceClient.Grep)
}

// ResolveAuthorityPath implements executorv1.FileServiceServer.
func (p *FileProxy) ResolveAuthorityPath(
	ctx context.Context,
	req *executorv1.ResolveAuthorityPathRequest,
) (*executorv1.ResolveAuthorityPathResponse, error) {
	return forwardUnary(ctx, p, req, executorv1.FileServiceClient.ResolveAuthorityPath)
}

// ReadFile relays chunks as they arrive, failing once they exceed the template's file limit.
func (p *FileProxy) ReadFile(
	req *executorv1.ReadFileRequest,
	stream grpc.ServerStreamingServer[executorv1.ReadFileResponse],
) error {
	ctx := stream.Context()
	a, err := p.proxy.open(ctx, req.GetContext())
	if err != nil {
		return err
	}
	defer a.release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	up, err := executorv1.NewFileServiceClient(a.conn).ReadFile(ctx, req)
	if err != nil {
		return executorError(err)
	}
	var total int64
	for {
		msg, err := up.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return executorError(err)
		}
		total += int64(len(msg.GetData()))
		if total > a.route.MaxFileBytes {
			return limitExceeded("file exceeds the execution template limit")
		}
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
}

type (
	createStream = grpc.ClientStreamingServer[
		executorv1.CreateFileRequest,
		executorv1.CreateFileResponse,
	]
	replaceStream = grpc.ClientStreamingServer[
		executorv1.ReplaceFileRequest,
		executorv1.ReplaceFileResponse,
	]
)

// CreateFile implements executorv1.FileServiceServer.
func (p *FileProxy) CreateFile(stream createStream) error {
	return forwardUpload(p, stream, executorv1.FileServiceClient.CreateFile)
}

// ReplaceFile implements executorv1.FileServiceServer.
func (p *FileProxy) ReplaceFile(stream replaceStream) error {
	return forwardUpload(p, stream, executorv1.FileServiceClient.ReplaceFile)
}

type uploadMessage[T any] interface {
	*T
	contextual
	GetData() []byte
}

// forwardUpload authorizes from the first message and relays chunks without
// assembling the file. Returning early cancels the executor stream, which
// then writes nothing.
func forwardUpload[Req any, Resp any, PReq uploadMessage[Req]](
	p *FileProxy,
	stream grpc.ClientStreamingServer[Req, Resp],
	open func(
		executorv1.FileServiceClient,
		context.Context,
		...grpc.CallOption,
	) (grpc.ClientStreamingClient[Req, Resp], error),
) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	ctx := stream.Context()
	a, err := p.proxy.open(ctx, PReq(first).GetContext())
	if err != nil {
		return err
	}
	defer a.release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	up, err := open(executorv1.NewFileServiceClient(a.conn), ctx)
	if err != nil {
		return executorError(err)
	}
	var total int64
	for msg := first; ; {
		total += int64(len(PReq(msg).GetData()))
		if total > a.route.MaxFileBytes {
			return limitExceeded("file exceeds the execution template limit")
		}
		if err := up.Send(msg); err != nil {
			if _, err = up.CloseAndRecv(); err == nil {
				err = io.ErrUnexpectedEOF
			}
			return executorError(err)
		}
		msg, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	resp, err := up.CloseAndRecv()
	if err != nil {
		return executorError(err)
	}
	return stream.SendAndClose(resp)
}
