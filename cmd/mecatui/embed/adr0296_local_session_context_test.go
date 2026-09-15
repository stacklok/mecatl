package embed

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
)

func adr0296MockAppConfig(t testing.TB, workspace string) app.Config {
	t.Helper()
	return app.Config{
		Workspace: workspace, UserModelDir: t.TempDir(), Model: "mock-model", UseMock: true,
		Shell: "/bin/sh", Compaction: "heuristic", Tokenizer: "heuristic",
	}
}

func TestADR_0296_EmbeddedMecatuiServesLocalSessionContext(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	srv, err := Start(ctx, adr0296MockAppConfig(t, workspace), PerfConfig{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	info, err := os.Stat(srv.dir)
	if err != nil {
		t.Fatalf("Stat embedded socket directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("embedded socket directory mode = %04o, want 0700", got)
	}
	socket, err := os.Lstat(filepath.Join(srv.dir, socketName))
	if err != nil {
		t.Fatalf("Stat embedded socket: %v", err)
	}
	if got := socket.Mode().Perm(); got != 0o600 {
		t.Fatalf("embedded socket mode = %04o, want 0600", got)
	}

	conn, err := grpc.NewClient(srv.Target(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial embedded socket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	harness := mecatlv1.NewHarnessServiceClient(conn)
	created, err := harness.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := mecatlv1.NewLocalSessionContextServiceClient(conn).GetLocalSessionContext(ctx, &mecatlv1.GetLocalSessionContextRequest{SessionId: created.GetSessionId()})
	if err != nil {
		t.Fatalf("GetLocalSessionContext: %v", err)
	}
	want, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("canonicalize workspace path: %v", err)
	}
	if got.GetWorkspacePath() != want {
		t.Fatalf("workspace path = %q, want %q", got.GetWorkspacePath(), want)
	}
}

func TestADR_0296_LocalContextRequiresEmbeddedPrivateListener(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(t *testing.T) net.Listener
	}{
		{name: "tcp", open: func(t *testing.T) net.Listener {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			return l
		}},
		{name: "world-readable-parent", open: func(t *testing.T) net.Listener {
			lis, dir, _, err := newUnixSocketListener()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			return lis
		}},
		{name: "world-readable-socket", open: func(t *testing.T) net.Listener {
			lis, _, sock, err := newUnixSocketListener()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(sock, 0o666); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(sock)) })
			return lis
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lis := tc.open(t)
			defer func() { _ = lis.Close() }()
			grpcSrv := grpc.NewServer()
			if err := registerLocalSessionContextServer(grpcSrv, lis, server.NewLocalSessionContextServer(nil)); err == nil {
				t.Fatal("registered local context service on an ineligible listener")
			}
			if _, found := grpcSrv.GetServiceInfo()[mecatlv1.LocalSessionContextService_ServiceDesc.ServiceName]; found {
				t.Fatal("ineligible listener registered local context service")
			}
		})
	}
}

func TestADR_0296_StandaloneMecatedDoesNotExposeLocalSessionContext(t *testing.T) {
	ctx := context.Background()
	// Standalone registration is intentionally absent: an ordinary gRPC server with
	// the standard Harness registration leaves this privileged method unimplemented.
	grpcSrv := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcSrv, server.NewHarnessServer(nil))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = lis.Close() }()
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_, err = mecatlv1.NewLocalSessionContextServiceClient(conn).GetLocalSessionContext(ctx, &mecatlv1.GetLocalSessionContextRequest{SessionId: "session"})
	if got := status.Code(err); got != codes.Unimplemented {
		t.Fatalf("standalone local context RPC code = %v, want %v", got, codes.Unimplemented)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/sessions/session/local-context", nil)
	response := httptest.NewRecorder()
	server.NewHTTPHandler(nil).ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("standalone local context HTTP route status = %d, want %d", response.Code, http.StatusNotFound)
	}
}
