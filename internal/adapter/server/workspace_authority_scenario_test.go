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
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
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
		MemberEngine: func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
			catalog := tool.NewCatalog()
			for _, candidate := range agent.MemberTools(tm, spec.Name, nil) {
				catalog.MustRegister(candidate)
			}
			return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
				LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: catalog, Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model",
			})}
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// TestWorkspaceAuthorityConfigRejectsUnrepresentableCombinations pins the reason
// the file-less deployment is a third authority value rather than a separate
// bool: every meaningful combination of authority and configured root is
// expressible, and the meaningless ones fail ONCE at construction instead of on
// every request. A bool would let "server-assigned, root not configured" build.
func TestWorkspaceAuthorityConfigRejectsUnrepresentableCombinations(t *testing.T) {
	base := func() server.Config {
		return server.Config{
			Engine: agent.NewEngine(agent.Deps{
				LLM:     mockllm.New(mockllm.TextTurn("ok")),
				Catalog: tool.NewCatalog(),
				Policy:  permpolicy.NewPolicy(nil, nil),
				Model:   "test-model",
			}),
			Store:      memstore.New(),
			Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		}
	}
	for _, tc := range []struct {
		name      string
		authority server.WorkspaceAuthority
		root      string
		wantErr   bool
	}{
		{name: "client-selected ignores an incidental root", authority: server.WorkspaceAuthorityClientSelected, root: deploymentWorkspace},
		{name: "client-selected without a root", authority: server.WorkspaceAuthorityClientSelected},
		{name: "server-assigned with a clean absolute root", authority: server.WorkspaceAuthorityServerAssigned, root: deploymentWorkspace},
		{name: "server-assigned without a root", authority: server.WorkspaceAuthorityServerAssigned, wantErr: true},
		{name: "server-assigned with a relative root", authority: server.WorkspaceAuthorityServerAssigned, root: "relative/root", wantErr: true},
		{name: "server-assigned with an unclean root", authority: server.WorkspaceAuthorityServerAssigned, root: "/deployment/../deployment/workspace", wantErr: true},
		{name: "file-less without a root", authority: server.WorkspaceAuthorityFileless},
		{name: "file-less with a root", authority: server.WorkspaceAuthorityFileless, root: deploymentWorkspace, wantErr: true},
		{name: "unknown authority", authority: server.WorkspaceAuthority(42), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			cfg.WorkspaceAuthority = tc.authority
			cfg.AuthoritativeWorkspace = tc.root
			_, err := server.NewService(cfg)
			if tc.wantErr {
				if !errors.Is(err, server.ErrConfig) {
					t.Fatalf("NewService err = %v, want ErrConfig", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}
		})
	}
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

func TestListenerScopedWorkspaceAuthority_Scenario1_CreateTeamGrpcAndHTTPAgree(t *testing.T) {
	var roots []string
	svc := serverAssignedAuthorityService(t, nil, func(root string) tool.Workspace {
		roots = append(roots, root)
		return memfs.NewWorkspace(root)
	})
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	httpServer := httptest.NewServer(server.NewHTTPHandler(svc))
	defer httpServer.Close()

	created, err := client.CreateTeam(context.Background(), &mecatlv1.CreateTeamRequest{})
	if err != nil {
		t.Fatalf("gRPC empty CreateTeam: %v", err)
	}
	if created.GetTeamId() == "" {
		t.Fatal("gRPC empty CreateTeam returned an empty team id")
	}
	if got := strings.Join(roots, ","); got != deploymentWorkspace {
		t.Fatalf("gRPC CreateTeam workspace roots = %q, want %q", got, deploymentWorkspace)
	}

	grpcCalls, grpcRoots := len(roots), strings.Join(roots, ",")
	_, err = client.CreateTeam(context.Background(), &mecatlv1.CreateTeamRequest{Workspace: "/client/root"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("gRPC CreateTeam with client workspace error = %v, want InvalidArgument", err)
	}
	if len(roots) != grpcCalls || strings.Join(roots, ",") != grpcRoots {
		t.Fatalf("rejected gRPC CreateTeam constructed workspace: before=%q after=%q", grpcRoots, strings.Join(roots, ","))
	}

	resp, err := http.Post(httpServer.URL+"/v1/teams", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("HTTP empty CreateTeam: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("HTTP empty CreateTeam status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if got := strings.Join(roots, ","); got != deploymentWorkspace+","+deploymentWorkspace {
		t.Fatalf("HTTP CreateTeam workspace roots = %q, want deployment workspace for both successful calls", got)
	}

	httpCalls, httpRoots := len(roots), strings.Join(roots, ",")
	resp, err = http.Post(httpServer.URL+"/v1/teams", "application/json", strings.NewReader(`{"workspace":"/client/root"}`))
	if err != nil {
		t.Fatalf("HTTP CreateTeam with client workspace: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("HTTP CreateTeam with client workspace status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	if len(roots) != httpCalls || strings.Join(roots, ",") != httpRoots {
		t.Fatalf("rejected HTTP CreateTeam constructed workspace: before=%q after=%q", httpRoots, strings.Join(roots, ","))
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

func TestListenerScopedWorkspaceAuthority_Scenario5_ScheduledFireCannotReviveOffRoot(t *testing.T) {
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	var workspaceCalls atomic.Int32
	svc := serverAssignedScheduleService(t, store, func(root string) tool.Workspace {
		workspaceCalls.Add(1)
		return memfs.NewWorkspace(root)
	})

	created, err := svc.CreateSchedule(context.Background(), port.ScheduleSpec{
		Name:      "server-root",
		Prompt:    "run",
		Trigger:   port.TriggerSpec{Cron: "@every 1h"},
		Mode:      session.ModePlan,
		Workspace: "",
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if created.Spec.Workspace != "" {
		t.Fatalf("created schedule workspace = %q, want empty (the wire value; the fire assigns the deployment root)", created.Spec.Workspace)
	}
	// Round-trip: the persisted schedule workspace must survive re-entry into the
	// create gate the fire path uses. An empty stored workspace mints a session on
	// the deployment root; persisting the resolved root instead made the fire fail
	// with InvalidArgument, so the schedule could never fire.
	fireSess, err := svc.CreateSessionWithProfile(context.Background(), created.Spec.Workspace, session.ModePlan, session.Limits{}, server.ProviderSelector{}, server.SessionProfile(created.Spec.Profile))
	if err != nil {
		t.Fatalf("re-create session from persisted schedule spec (the fire path): %v", err)
	}
	if fireSess.Workspace != deploymentWorkspace {
		t.Fatalf("fire session workspace = %q, want deployment root %q", fireSess.Workspace, deploymentWorkspace)
	}
	if _, err := svc.CreateSchedule(context.Background(), port.ScheduleSpec{
		Name: "client-root", Prompt: "run", Trigger: port.TriggerSpec{Cron: "@every 1h"}, Mode: session.ModePlan, Workspace: "/client/root",
	}); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("CreateSchedule with client workspace error = %v, want InvalidArgument", err)
	}
	noFS, err := svc.CreateSchedule(context.Background(), port.ScheduleSpec{
		Name: "no-fs", Prompt: "run", Trigger: port.TriggerSpec{Cron: "@every 1h"}, Mode: session.ModePlan, Profile: string(server.ProfileNoFS),
	})
	if err != nil {
		t.Fatalf("CreateSchedule(no-fs): %v", err)
	}
	if noFS.Spec.Workspace != "" {
		t.Fatalf("no-fs schedule workspace = %q, want empty", noFS.Spec.Workspace)
	}

	legacy := port.Schedule{Spec: port.ScheduleSpec{
		Name: "legacy-off-root", Prompt: "run", Trigger: port.TriggerSpec{Cron: "@every 1h"}, Mode: session.ModePlan, Workspace: "/legacy/off-root",
	}, State: port.ScheduleState{Enabled: true, NextFireAt: time.Now().Add(time.Hour)}}
	if err := store.ScheduleStore().Save(context.Background(), legacy); err != nil {
		t.Fatalf("save legacy schedule: %v", err)
	}
	var fired atomic.Int32
	sch := scheduler.New(scheduler.Config{
		Store: store.ScheduleStore(), Clock: wallclock.Clock{}, Diagnostics: port.NopDiagnostics{},
		Fire: func(context.Context, port.Schedule, time.Time) (port.ScheduleFire, error) {
			fired.Add(1)
			return port.ScheduleFire{}, nil
		},
	})
	svc.SetScheduler(sch)
	if _, err := svc.FireNow(context.Background(), legacy.Spec.Name); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("FireNow legacy off-root error = %v, want FailedPrecondition", err)
	}
	if got := fired.Load(); got != 0 {
		t.Fatalf("legacy off-root fire callback calls = %d, want 0", got)
	}
	stored, err := store.ScheduleStore().Load(context.Background(), legacy.Spec.Name)
	if err != nil {
		t.Fatalf("load legacy schedule: %v", err)
	}
	if stored.State.FireCount != 0 {
		t.Fatalf("legacy off-root schedule fire count = %d, want 0 before rejection", stored.State.FireCount)
	}
	if got := workspaceCalls.Load(); got != 0 {
		t.Fatalf("workspace factory calls = %d, want 0 before legacy schedule rejection", got)
	}
}

func TestListenerScopedWorkspaceAuthority_Scenario5_AllCreationPathsRespectAuthority(t *testing.T) {
	store := memstore.New()
	var factoryCalls atomic.Int32
	var workspaceCalls atomic.Int32
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model"})
	// The counted factory proves adoption and CreateTeam reject off-root bindings
	// before engine or environment construction can receive them.
	svc, err := server.NewService(server.Config{
		Engine: eng,
		Store:  store,
		Workspaces: func(root string) tool.Workspace {
			workspaceCalls.Add(1)
			return memfs.NewWorkspace(root)
		},
		WorkspaceAuthority: server.WorkspaceAuthorityServerAssigned, AuthoritativeWorkspace: deploymentWorkspace,
		OwnershipEnforced: true,
		MemberEngine: func(_ *team.Team, _ agent.MemberSpec, _ string) agent.MemberBuild {
			return agent.MemberBuild{Engine: eng}
		},
		SessionEngine: func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
			factoryCalls.Add(1)
			return server.SessionEngineResult{}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	_, err = server.NewHarnessServer(svc).CreateTeam(context.Background(), &mecatlv1.CreateTeamRequest{Workspace: "/client/root"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateTeam with client workspace code = %v, want InvalidArgument (err=%v)", status.Code(err), err)
	}
	if got := workspaceCalls.Load(); got != 0 {
		t.Fatalf("workspace factory calls = %d, want 0 before rejected CreateTeam construction", got)
	}
	legacy := session.New("legacy-adoption", session.ModeDefault, "/legacy", session.Limits{MaxTurns: 5}, time.Unix(0, 0))
	if err := legacy.RestoreSessionMetadata(session.SessionKindUnknown, session.SessionRelationship{}); err != nil {
		t.Fatalf("RestoreSessionMetadata: %v", err)
	}
	principal := &session.Principal{Issuer: "issuer", Subject: "subject", GrantType: session.GrantTypeUser}
	if err := legacy.RestoreLabels(principal, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if err := store.Save(context.Background(), legacy); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
	ctx := session.WithPrincipal(context.Background(), principal)
	bindings := server.AdoptionBindings{
		Workspace: "/legacy/off-root", EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/legacy/off-root"},
		ProviderID: "provider", ModelID: "model", Profile: server.ProfileDefault,
	}
	preflight, err := svc.PreflightSessionAdoption(ctx, legacy.ID, bindings)
	if err != nil {
		t.Fatalf("PreflightSessionAdoption: %v", err)
	}
	if preflight.Eligible || preflight.Reason != server.AdoptionReasonBindingUnresolved {
		t.Fatalf("off-root adoption preflight = %+v, want binding_unresolved", preflight)
	}
	if got := factoryCalls.Load(); got != 0 {
		t.Fatalf("session engine factory calls = %d, want 0 before off-root adoption rejection", got)
	}
}

func serverAssignedScheduleService(t *testing.T, store *jsonlstore.Store, workspaces server.WorkspaceFactory) *server.Service {
	t.Helper()
	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model"}),
		Store:  store, Workspaces: workspaces, WorkspaceAuthority: server.WorkspaceAuthorityServerAssigned, AuthoritativeWorkspace: deploymentWorkspace,
		Now: func() time.Time { return time.Unix(0, 0) }, Diagnostics: port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// TestListenerScopedWorkspaceAuthority_Scenario5_ForkOffRootFailsClosed pins that
// ForkSession is gated by the same persisted-workspace authority as every other
// recovery route (issue found in panel review): a stale off-root source must be
// rejected before the fork is persisted or a per-session engine (and its
// workspace-scoped ingestion) is built. The gate lives at the shared
// reopenLoadedSession choke point, so this proves the funnel covers fork.
func TestListenerScopedWorkspaceAuthority_Scenario5_ForkOffRootFailsClosed(t *testing.T) {
	store := memstore.New()
	var engineCalls atomic.Int32
	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(),
			Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model",
		}),
		Store:                  store,
		Workspaces:             func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		DefaultLimits:          session.Limits{MaxTurns: 5},
		Now:                    func() time.Time { return time.Unix(0, 0) },
		WorkspaceAuthority:     server.WorkspaceAuthorityServerAssigned,
		AuthoritativeWorkspace: deploymentWorkspace,
		OwnershipEnforced:      true,
		SessionEngine: func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
			engineCalls.Add(1)
			return server.SessionEngineResult{Close: func() error { return nil }}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// A stale off-root source: persisted with a root the deployment no longer
	// assigns (e.g. created under an earlier client-selected configuration).
	owner := &session.Principal{Issuer: "iss", Subject: "sub", GrantType: session.GrantTypeUser}
	src := session.New("fork-src-off-root", session.ModeDefault, "/other/root", session.Limits{MaxTurns: 5}, time.Unix(0, 0))
	if err := src.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if err := store.Save(context.Background(), src); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	ctx := session.WithPrincipal(context.Background(), owner)
	if _, err := svc.ForkSession(ctx, src.ID, "", ""); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("ForkSession off-root error = %v, want FailedPrecondition", err)
	}
	if got := engineCalls.Load(); got != 0 {
		t.Fatalf("session engine factory calls = %d, want 0 before the off-root fork is rehydrated", got)
	}
}
