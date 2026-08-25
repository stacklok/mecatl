package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const deploymentWorkspace = "/deployment/workspace"

func serverAssignedAuthorityService(t *testing.T, store *memstore.Store, workspaces server.WorkspaceFactory, resolvers ...func(context.Context, session.EnvironmentRef) (tool.Environment, error)) *server.Service {
	t.Helper()
	var resolver func(context.Context, session.EnvironmentRef) (tool.Environment, error)
	if len(resolvers) > 0 {
		resolver = resolvers[0]
	}
	if store == nil {
		store = memstore.New()
	}
	if workspaces == nil {
		workspaces = func(root string) tool.Workspace { return memfs.NewWorkspace(root) }
	}
	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("ok")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		}),
		Store:                  store,
		Workspaces:             workspaces,
		DefaultLimits:          session.Limits{MaxTurns: 5},
		Now:                    func() time.Time { return time.Unix(0, 0) },
		WorkspaceAuthority:     server.WorkspaceAuthorityServerAssigned,
		AuthoritativeWorkspace: deploymentWorkspace,
		EnvironmentResolver:    resolver,
		SessionEngine: func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{
				Engine: agent.NewEngine(agent.Deps{
					LLM:     mockllm.New(mockllm.TextTurn("ok")),
					Catalog: tool.NewCatalog(),
					Policy:  permpolicy.NewPolicy(nil, nil),
					Model:   "test-model",
				}),
				Close: func() error { return nil },
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestListenerScopedWorkspaceAuthority_Scenario1_EmptyWorkspaceUsesConfiguredRoot(t *testing.T) {
	svc := serverAssignedAuthorityService(t, nil, nil)

	sess, err := svc.CreateSession(context.Background(), "", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Workspace != deploymentWorkspace {
		t.Fatalf("session workspace = %q, want configured deployment root %q", sess.Workspace, deploymentWorkspace)
	}
}

func TestInvariant_server_assigned_workspace_requires_empty_request(t *testing.T) {
	var workspaceCalls atomic.Int32
	svc := serverAssignedAuthorityService(t, nil, func(root string) tool.Workspace {
		workspaceCalls.Add(1)
		return memfs.NewWorkspace(root)
	})

	for _, requested := range []string{" ", "relative", "../traversal", deploymentWorkspace} {
		t.Run(requested, func(t *testing.T) {
			_, err := svc.CreateSession(context.Background(), requested, session.ModeDefault, session.Limits{})
			if !errors.Is(err, server.ErrInvalidArgument) {
				t.Fatalf("CreateSession(%q) error = %v, want InvalidArgument", requested, err)
			}
			if !strings.Contains(err.Error(), "deployment assigns the workspace") {
				t.Fatalf("CreateSession(%q) error = %q, want deployment-assigned explanation", requested, err)
			}
		})
	}
	if got := workspaceCalls.Load(); got != 0 {
		t.Fatalf("workspace factory calls = %d, want 0 for rejected requests", got)
	}
}

func TestListenerScopedWorkspaceAuthority_Scenario1_ServerAssignedProfileMatrix(t *testing.T) {
	svc := serverAssignedAuthorityService(t, nil, nil)

	if _, err := svc.CreateSessionWithProfile(context.Background(), "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS); err != nil {
		t.Fatalf("CreateSessionWithProfile(no-fs, empty workspace): %v", err)
	}
	if _, err := svc.CreateSessionWithProfile(context.Background(), "not-empty", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS); !errors.Is(err, server.ErrInvalidArgument) || !strings.Contains(err.Error(), "must not carry a workspace") {
		t.Fatalf("CreateSessionWithProfile(no-fs, non-empty workspace) error = %v, want existing no-FS InvalidArgument", err)
	}
}

func TestListenerScopedWorkspaceAuthority_Scenario1_GrpcAndHTTPAgree(t *testing.T) {
	svc := serverAssignedAuthorityService(t, nil, nil)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	_, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{Workspace: "client-path"})
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), "deployment assigns the workspace") {
		t.Fatalf("gRPC CreateSession error = %v, want InvalidArgument deployment-assigned explanation", err)
	}

	httpServer := httptest.NewServer(server.NewHTTPHandler(svc))
	defer httpServer.Close()
	resp, err := http.Post(httpServer.URL+"/v1/sessions", "application/json", strings.NewReader(`{"workspace":"client-path"}`))
	if err != nil {
		t.Fatalf("HTTP CreateSession: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("HTTP CreateSession status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	grpcCreated, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("gRPC empty CreateSession: %v", err)
	}
	if grpcCreated.GetSessionId() == "" {
		t.Fatal("gRPC empty CreateSession returned an empty session id")
	}
	resp, err = http.Post(httpServer.URL+"/v1/sessions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("HTTP empty CreateSession: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("HTTP empty CreateSession status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
}

func TestListenerScopedWorkspaceAuthority_Scenario5_PersistedOffRootSessionFailsClosed(t *testing.T) {
	store := memstore.New()
	var workspaceCalls atomic.Int32
	var resolverCalls atomic.Int32
	svc := serverAssignedAuthorityService(t, store, func(root string) tool.Workspace {
		workspaceCalls.Add(1)
		return memfs.NewWorkspace(root)
	}, func(_ context.Context, _ session.EnvironmentRef) (tool.Environment, error) {
		resolverCalls.Add(1)
		return tool.Environment{}, nil
	})
	sess := session.New("off-root", session.ModeDefault, "/other/root", session.Limits{MaxTurns: 5}, time.Unix(0, 0))
	sess.EnvironmentRef = session.EnvironmentRef{Kind: "remote", ID: "off-root"}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	if _, err := svc.StartRun(context.Background(), sess.ID, "continue"); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("StartRun error = %v, want FailedPrecondition", err)
	}
	if got := workspaceCalls.Load(); got != 0 {
		t.Fatalf("workspace factory calls = %d, want 0 before persisted-root rejection", got)
	}
	if got := resolverCalls.Load(); got != 0 {
		t.Fatalf("environment resolver calls = %d, want 0 before persisted-root rejection", got)
	}

	// An override is another route into a live workspace. Even with an on-root
	// persisted session, its own root cannot widen server-assigned authority.
	overrideSession := session.New("override-off-root", session.ModeDefault, deploymentWorkspace, session.Limits{MaxTurns: 5}, time.Unix(0, 0))
	if err := store.Save(context.Background(), overrideSession); err != nil {
		t.Fatalf("store.Save override session: %v", err)
	}
	svc.SetSessionEnvironment(overrideSession.ID, tool.MustEnvironment(
		session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/other/root"}, memfs.NewWorkspace("/other/root"), nil,
	))
	if _, err := svc.StartRun(context.Background(), overrideSession.ID, "continue"); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("StartRun with off-root environment override error = %v, want FailedPrecondition", err)
	}
	if got := workspaceCalls.Load(); got != 0 {
		t.Fatalf("workspace factory calls = %d, want 0 before environment override rejection", got)
	}
}

func TestListenerScopedWorkspaceAuthority_Scenario5_PersistedRootIdentityIsLexicalAndFailClosed(t *testing.T) {
	configured := filepath.Clean(deploymentWorkspace)
	for _, stored := range []string{
		"relative/root",
		deploymentWorkspace + "/../workspace",
		deploymentWorkspace + "-symlink-alias",
		"/old/deployment/workspace",
	} {
		t.Run(stored, func(t *testing.T) {
			store := memstore.New()
			var workspaceCalls atomic.Int32
			svc := serverAssignedAuthorityService(t, store, func(root string) tool.Workspace {
				workspaceCalls.Add(1)
				return memfs.NewWorkspace(root)
			})
			sess := session.New(session.SessionID("stored-"+strings.ReplaceAll(stored, "/", "-")), session.ModeDefault, stored, session.Limits{MaxTurns: 5}, time.Unix(0, 0))
			if err := store.Save(context.Background(), sess); err != nil {
				t.Fatalf("store.Save: %v", err)
			}
			if _, err := svc.StartRun(context.Background(), sess.ID, "continue"); !errors.Is(err, server.ErrFailedPrecondition) {
				t.Fatalf("StartRun error = %v, want FailedPrecondition", err)
			}
			if got := workspaceCalls.Load(); got != 0 {
				t.Fatalf("workspace factory calls = %d, want 0", got)
			}
		})
	}

	store := memstore.New()
	var gotRoot string
	svc := serverAssignedAuthorityService(t, store, func(root string) tool.Workspace {
		gotRoot = root
		return memfs.NewWorkspace(root)
	})
	sess := session.New("on-root", session.ModeDefault, configured, session.Limits{MaxTurns: 5}, time.Unix(0, 0))
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "continue")
	if err != nil {
		t.Fatalf("StartRun on configured root: %v", err)
	}
	_ = drainServerRun(run)
	if gotRoot != configured {
		t.Fatalf("workspace factory root = %q, want exact configured root %q", gotRoot, configured)
	}
}
