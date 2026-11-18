package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/internal/executorrpc/fileconformance"
)

func TestFileServiceOverOSFSConformance(t *testing.T) {
	fileconformance.Run(t, func(t *testing.T) fileconformance.Target {
		root := t.TempDir()
		srv, err := newFileServer(root, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		return fileconformance.Target{
			Client: fileconformance.Dial(t, srv),
			Root:   root,
			External: fsconformance.ExternalAccess{
				Read: func(p string) ([]byte, error) {
					return os.ReadFile(filepath.Join(root, p))
				},
				Write: func(p string, data []byte) error {
					return os.WriteFile(filepath.Join(root, p), data, 0o600)
				},
			},
		}
	})
}

func TestFileServiceOverOSFSBoundsFileSize(t *testing.T) {
	root := t.TempDir()
	srv, err := newFileServer(root, 4)
	if err != nil {
		t.Fatal(err)
	}
	ws := fileconformance.NewWorkspace(fileconformance.Dial(t, srv), root)
	big := bytes.Repeat([]byte("x"), 5)
	if err := os.WriteFile(filepath.Join(root, "big"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Read(context.Background(), "big"); err == nil {
		t.Fatal("read of an oversized file succeeded")
	}
	if _, err := ws.CreateFile(context.Background(), "new", []byte("12345")); err == nil {
		t.Fatal("oversized create succeeded")
	}
}

func TestServeRejectsInvalidMaxFileBytes(t *testing.T) {
	for _, value := range []string{"0", "5242881"} {
		args := []string{serveMode, "-listen", "127.0.0.1:0", "-files", "-max-file-bytes", value}
		err := runServeMode(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "max-file-bytes") {
			t.Fatalf("-max-file-bytes %s: err = %v", value, err)
		}
	}
}

func TestServeRejectsNoEnabledService(t *testing.T) {
	err := runServeMode(context.Background(), []string{serveMode, "-listen", "127.0.0.1:0"})
	if err == nil || !strings.Contains(err.Error(), "enable at least one") {
		t.Fatalf("err = %v", err)
	}
}

func TestServeWithoutCommandsLeavesCommandServiceUnimplemented(t *testing.T) {
	ctx := context.Background()
	cfg := serveConfig{files: true, maxFileBytes: 1 << 20}
	srv, err := newExecutorServer(ctx, t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	_, err = executorv1.NewFileServiceClient(conn).StatFile(
		ctx,
		&executorv1.StatFileRequest{Path: "missing"},
	)
	if status.Code(err) == codes.Unimplemented {
		t.Fatalf("StatFile: %v", err)
	}

	stream, err := executorv1.NewCommandServiceClient(conn).RunCommand(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start := &executorv1.RunCommandRequest_Start{
		Start: &executorv1.StartCommand{Command: "true"},
	}
	_ = stream.Send(&executorv1.RunCommandRequest{Action: start})
	if _, err := stream.Recv(); status.Code(err) != codes.Unimplemented {
		t.Fatalf("RunCommand: err = %v, want Unimplemented", err)
	}
}
