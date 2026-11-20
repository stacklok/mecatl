package executionclient

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
	"github.com/stacklok/mecatl/internal/executorrpc/executorclient"
)

// ErrExecutorServicesMissing reports a provider that does not serve the
// executor FileService and CommandService, such as one from an older release.
var ErrExecutorServicesMissing = fmt.Errorf(
	"%w: execution provider does not serve the executor file and command services",
	server.ErrPlacementUnavailable,
)

// CheckExecutorServices reports whether the provider serves file and command calls.
func (c *Client) CheckExecutorServices(ctx context.Context) error {
	health := healthpb.NewHealthClient(c)
	for _, service := range []string{
		executorv1.FileService_ServiceDesc.ServiceName,
		executorv1.CommandService_ServiceDesc.ServiceName,
	} {
		resp, err := health.Check(ctx, &healthpb.HealthCheckRequest{Service: service})
		switch status.Code(err) {
		case codes.OK:
		case codes.Unimplemented, codes.NotFound:
			return ErrExecutorServicesMissing
		default:
			return mapPlacementError(decodeError(ctx, err))
		}
		if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			return ErrExecutorServicesMissing
		}
	}
	return nil
}

// Workspace returns the files of the run rc names, through the provider.
func (c *Client) Workspace(rc executionenv.RequestContext) *executorclient.Workspace {
	return executorclient.NewWorkspace(c, remoteRoot, fixedContext(rc))
}

// Commands returns the foreground commands of the run rc names, through the provider.
func (c *Client) Commands(rc executionenv.RequestContext) *executorclient.Runner {
	return executorclient.NewRunner(c, remoteRoot, fixedContext(rc))
}

func fixedContext(rc executionenv.RequestContext) executorclient.RequestContextFunc {
	return func(context.Context) (*executorv1.RequestContext, error) {
		return executorContext(rc), nil
	}
}

// Refusal returns the provider's refusal of a file or command call that never
// reached the executor, and nil for any other error.
func Refusal(err error) *executionenv.Error {
	st, ok := status.FromError(err)
	if !ok {
		return nil
	}
	var forwarded bool
	var detail *executionv1.ErrorDetail
	for _, d := range st.Details() {
		switch d := d.(type) {
		case *executorv1.ErrorDetail:
			forwarded = forwarded ||
				d.GetReason() == executorv1.ErrorReason_ERROR_REASON_NOT_FORWARDED
		case *executionv1.ErrorDetail:
			detail = d
		}
	}
	if !forwarded || detail == nil {
		return nil
	}
	code := executionenv.ErrorCode(detail.GetCode())
	if !code.Valid() || st.Code() != grpcCodeForError(code) {
		return nil
	}
	return &executionenv.Error{
		Code:      code,
		Message:   genericProviderMessage,
		Retryable: detail.GetRetryable(),
	}
}

func staleRunClaim(err error) bool {
	refusal := Refusal(err)
	return refusal != nil && refusal.Code == executionenv.CodeUnauthenticated && refusal.Retryable
}

// runWorkspace renews the run claim and retries a read-only call once when
// the provider refuses the claim as stale. Calls may run concurrently.
type runWorkspace struct {
	*executorclient.Workspace
	credentials *runContext
}

var (
	_ tool.Workspace                 = (*runWorkspace)(nil)
	_ tool.WorkspaceNamespace        = (*runWorkspace)(nil)
	_ tool.AuthorityResourceResolver = (*runWorkspace)(nil)
)

func retryRead[T any](ctx context.Context, w *runWorkspace, call func() (T, error)) (T, error) {
	out, err := call()
	if !staleRunClaim(err) {
		return out, err
	}
	if _, refreshErr := w.credentials.refresh(ctx); refreshErr != nil {
		var zero T
		return zero, refreshErr
	}
	return call()
}

func (w *runWorkspace) Read(ctx context.Context, path string) ([]byte, error) {
	return retryRead(ctx, w, func() ([]byte, error) { return w.Workspace.Read(ctx, path) })
}

func (w *runWorkspace) ReadVersion(
	ctx context.Context,
	path string,
) ([]byte, tool.FileVersion, error) {
	type read struct {
		data    []byte
		version tool.FileVersion
	}
	out, err := retryRead(ctx, w, func() (read, error) {
		data, version, err := w.Workspace.ReadVersion(ctx, path)
		return read{data, version}, err
	})
	return out.data, out.version, err
}

func (w *runWorkspace) Stat(ctx context.Context, path string) (tool.FileInfo, error) {
	return retryRead(ctx, w, func() (tool.FileInfo, error) { return w.Workspace.Stat(ctx, path) })
}

func (w *runWorkspace) ReadDir(ctx context.Context, path string) ([]tool.FileInfo, error) {
	return retryRead(ctx, w, func() ([]tool.FileInfo, error) {
		return w.Workspace.ReadDir(ctx, path)
	})
}

func (w *runWorkspace) Glob(ctx context.Context, pattern string) ([]string, error) {
	return retryRead(ctx, w, func() ([]string, error) { return w.Workspace.Glob(ctx, pattern) })
}

func (w *runWorkspace) Grep(
	ctx context.Context,
	pattern string,
	pathGlob string,
) ([]tool.GrepMatch, error) {
	return retryRead(ctx, w, func() ([]tool.GrepMatch, error) {
		return w.Workspace.Grep(ctx, pattern, pathGlob)
	})
}

func (w *runWorkspace) AuthorityResourcePath(path string) (target, workspace string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), authorityResolveRequestTimeout)
	defer cancel()
	out, err := retryRead(ctx, w, func() ([2]string, error) {
		target, workspace, err := w.Workspace.AuthorityResourcePath(path)
		return [2]string{target, workspace}, err
	})
	return out[0], out[1], err
}
