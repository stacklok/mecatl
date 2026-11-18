package executorclient_test

import (
	"context"
	"net"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
	"github.com/stacklok/mecatl/internal/executorrpc/executorclient"
	"github.com/stacklok/mecatl/internal/executorrpc/fileserver"
	"github.com/stacklok/mecatl/internal/executorrpc/memexecutor"
)

const root = "/workspace"

type fixture struct {
	conn     *grpc.ClientConn
	mu       sync.Mutex
	netConns []net.Conn
	received []*executorv1.RequestContext
}

type contextual interface {
	GetContext() *executorv1.RequestContext
}

func (f *fixture) record(msg any) {
	if m, ok := msg.(contextual); ok {
		f.mu.Lock()
		f.received = append(f.received, proto.CloneOf(m.GetContext()))
		f.mu.Unlock()
	}
}

func (f *fixture) contexts() []*executorv1.RequestContext {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*executorv1.RequestContext(nil), f.received...)
}

func (f *fixture) dropConnections() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.netConns {
		_ = c.Close()
	}
}

type firstMessageStream struct {
	grpc.ServerStream
	f    *fixture
	once sync.Once
}

func (s *firstMessageStream) RecvMsg(m any) error {
	err := s.ServerStream.RecvMsg(m)
	if err == nil {
		s.once.Do(func() { s.f.record(m) })
	}
	return err
}

func serve(
	t *testing.T,
	files *memfs.Workspace,
	runner *memexecutor.Runner,
	opts ...grpc.DialOption,
) *fixture {
	t.Helper()
	f := &fixture{}
	gs := grpc.NewServer(
		grpc.UnaryInterceptor(func(
			ctx context.Context,
			req any,
			_ *grpc.UnaryServerInfo,
			handler grpc.UnaryHandler,
		) (any, error) {
			f.record(req)
			return handler(ctx, req)
		}),
		grpc.StreamInterceptor(func(
			srv any,
			ss grpc.ServerStream,
			_ *grpc.StreamServerInfo,
			handler grpc.StreamHandler,
		) error {
			return handler(srv, &firstMessageStream{ServerStream: ss, f: f})
		}),
	)
	if files != nil {
		srv, err := fileserver.New(files, fileserver.WithPathEscapeErrors(memfs.ErrPathEscape))
		if err != nil {
			t.Fatal(err)
		}
		executorv1.RegisterFileServiceServer(gs, srv)
	}
	if runner != nil {
		srv, err := commandserver.New(runner)
		if err != nil {
			t.Fatal(err)
		}
		executorv1.RegisterCommandServiceServer(gs, srv)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		c, err := lis.DialContext(ctx)
		if err == nil {
			f.mu.Lock()
			f.netConns = append(f.netConns, c)
			f.mu.Unlock()
		}
		return c, err
	}
	opts = append(opts,
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDisableRetry(),
	)
	conn, err := grpc.NewClient("passthrough:///executor", opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f.conn = conn
	return f
}

func TestWorkspaceConformance(t *testing.T) {
	fixture := func(t *testing.T) (tool.Workspace, fsconformance.ExternalAccess) {
		backend := memfs.NewFileSystem(root)
		f := serve(t, memfs.NewWorkspaceOverFileSystem(backend), nil)
		external := fsconformance.ExternalAccess{
			Read: func(p string) ([]byte, error) {
				return backend.Read(context.Background(), p)
			},
			Write: func(p string, data []byte) error {
				return backend.Write(context.Background(), p, data)
			},
		}
		return executorclient.NewWorkspace(f.conn, root, nil), external
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

func TestEveryCallCarriesItsOwnRequestContext(t *testing.T) {
	runner := memexecutor.NewRunner()
	runner.Script("true", memexecutor.Exit("", "", 0))
	f := serve(t, memfs.NewWorkspace(root), runner)
	var mu sync.Mutex
	var sent []*executorv1.RequestContext
	rc := func(context.Context) (*executorv1.RequestContext, error) {
		mu.Lock()
		defer mu.Unlock()
		c := &executorv1.RequestContext{
			Environment:     &executorv1.EnvironmentRef{Id: "env", Revision: "rev"},
			Owner:           &executorv1.Owner{Issuer: "issuer", Subject: "alice"},
			BindingId:       "binding",
			Epoch:           uint64(len(sent) + 1),
			RunId:           "run",
			ClaimId:         "claim",
			GrantGeneration: 7,
		}
		sent = append(sent, c)
		return c, nil
	}
	ws := executorclient.NewWorkspace(f.conn, root, rc)
	ctx := t.Context()
	v, err := ws.CreateFile(ctx, "a.txt", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	calls := []func() error{
		func() error { _, _, err := ws.ReadVersion(ctx, "a.txt"); return err },
		func() error { _, err := ws.Stat(ctx, "a.txt"); return err },
		func() error { _, err := ws.ReplaceFile(ctx, "a.txt", v, []byte("two")); return err },
		func() error { _, err := ws.ReadDir(ctx, "."); return err },
		func() error { _, err := ws.Glob(ctx, "*.txt"); return err },
		func() error { _, err := ws.Grep(ctx, "two", ""); return err },
		func() error { _, err := ws.CopyFile(ctx, "a.txt", "b.txt"); return err },
		func() error { return ws.Rename(ctx, "b.txt", "c.txt") },
		func() error { return ws.Remove(ctx, "c.txt") },
		func() error { _, _, err := ws.AuthorityResourcePath("a.txt"); return err },
		func() error {
			_, err := executorclient.NewRunner(f.conn, root, rc).Run(ctx, "true")
			return err
		},
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	got := f.contexts()
	if len(got) != len(sent) || len(sent) != len(calls)+1 {
		t.Fatalf("server saw %d contexts, client built %d for %d calls",
			len(got), len(sent), len(calls)+1)
	}
	for i := range sent {
		if !proto.Equal(got[i], sent[i]) {
			t.Fatalf("call %d context = %v, want %v", i, got[i], sent[i])
		}
	}
}
