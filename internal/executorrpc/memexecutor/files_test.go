package memexecutor_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/internal/executorrpc/fileconformance"
	"github.com/stacklok/mecatl/internal/executorrpc/memexecutor"
)

const root = "/workspace"

func TestFileServiceConformance(t *testing.T) {
	fileconformance.Run(t, func(t *testing.T) fileconformance.Target {
		backend := memfs.NewFileSystem(root)
		conn := memexecutor.Dial(t, memexecutor.Executor{
			Files: memfs.NewWorkspaceOverFileSystem(backend),
		})
		return fileconformance.Target{
			Conn: conn,
			Root: root,
			External: fsconformance.ExternalAccess{
				Read: func(p string) ([]byte, error) {
					return backend.Read(context.Background(), p)
				},
				Write: func(p string, data []byte) error {
					return backend.Write(context.Background(), p, data)
				},
			},
		}
	})
}

func TestPathEscapeIsReported(t *testing.T) {
	conn := memexecutor.Dial(t, memexecutor.Executor{Files: memfs.NewWorkspace(root)})
	_, err := executorv1.NewFileServiceClient(conn).StatFile(t.Context(),
		&executorv1.StatFileRequest{Path: "../outside"})
	if status.Code(err) != codes.PermissionDenied ||
		fileconformance.Reason(err) != executorv1.ErrorReason_ERROR_REASON_PATH_ESCAPE {
		t.Fatalf("StatFile = %v, want path escape", err)
	}
}

func TestUnregisteredCommandServiceIsUnimplemented(t *testing.T) {
	conn := memexecutor.Dial(t, memexecutor.Executor{Files: memfs.NewWorkspace(root)})
	stream, err := executorv1.NewCommandServiceClient(conn).RunCommand(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.Unimplemented {
		t.Fatalf("RunCommand = %v, want Unimplemented", err)
	}
}
