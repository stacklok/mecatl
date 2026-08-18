package client

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

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

func adoptionSeamService(t *testing.T) (*server.Service, *memstore.Store) {
	t.Helper()
	store := memstore.New()
	factory := func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
		eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: sel.ModelID})
		return server.SessionEngineResult{Engine: eng, ProviderID: sel.ProviderID, ModelID: sel.ModelID, BuiltForMode: mode, Close: func() error { return nil }}, nil
	}
	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}),
		Store:  store, Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) }, SessionEngine: factory,
		DefaultResolvedModel: server.ResolvedModel{ProviderID: "provider-a", ModelID: "model-a"}, OwnershipEnforced: true,
		StorageManagementAuthorized:         func(context.Context) bool { return true },
		LocalStorageMaintenanceSingleWriter: true,
		Now:                                 func() time.Time { return time.Unix(1700000000, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, store
}

func adoptionSeamClient(t *testing.T, svc *server.Service, subject string) *Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	principal := &session.Principal{Issuer: "https://idp.example", Subject: subject, GrantType: session.GrantTypeUser}
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(session.WithPrincipal(ctx, principal), req)
	}))
	mecatlv1.RegisterHarnessServiceServer(grpcServer, server.NewHarnessServer(svc))
	go func() { _ = grpcServer.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///adoption-ui", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = listener.Close()
	})
	return &Client{conn: conn, svc: mecatlv1.NewHarnessServiceClient(conn)}
}

func saveAdoptionSeamLegacy(t *testing.T, store *memstore.Store, id string, state session.State) *session.Session {
	t.Helper()
	sess := session.New(session.SessionID(id), session.ModeDefault, "/legacy", session.Limits{MaxTurns: 5}, time.Unix(1, 0))
	if err := sess.RestoreSessionMetadata(session.SessionKindUnknown, session.SessionRelationship{}); err != nil {
		t.Fatal(err)
	}
	if err := sess.RestoreLabels(&session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}, ""); err != nil {
		t.Fatal(err)
	}
	sess.ProviderID, sess.ModelID = "provider-a", "model-a"
	if err := sess.RecordUserPrompt("legacy question", nil); err != nil {
		t.Fatal(err)
	}
	if state != session.StateIdle {
		if err := sess.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		if state == session.StateCompleted {
			if err := sess.RecordAssistant(session.Message{Role: session.RoleAssistant, Text: "legacy answer"}); err != nil {
				t.Fatal(err)
			}
			if err := sess.Complete(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestMaintenanceClientRealServerSeam(t *testing.T) {
	svc, _ := adoptionSeamService(t)
	cl := adoptionSeamClient(t, svc, "alice")

	plan, err := cl.PlanSessionMigration(context.Background())
	if err != nil {
		t.Fatalf("plan migration over real gRPC seam: %v", err)
	}
	if plan.Available || plan.UnavailableReason != "backend_unsupported" {
		t.Fatalf("unsupported backend projection = %+v", plan)
	}

	cleanup, err := cl.PlanSessionCleanup(context.Background(), CleanupScope{Kinds: []string{"main"}})
	if err != nil {
		t.Fatalf("plan cleanup over real gRPC seam: %v", err)
	}
	if !cleanup.Available || cleanup.Protected.ByKind == nil {
		t.Fatalf("cleanup projection lost capability/taxonomy: %+v", cleanup)
	}
}

func TestAdoptionClientRealServerSeam(t *testing.T) {
	svc, store := adoptionSeamService(t)
	alice := adoptionSeamClient(t, svc, "alice")
	bob := adoptionSeamClient(t, svc, "bob")
	bindings := AdoptionBindings{Workspace: "/target", EnvironmentKind: "local", EnvironmentID: "/target", ProviderID: "provider-b", ModelID: "model-b"}

	eligible := saveAdoptionSeamLegacy(t, store, "eligible", session.StateCompleted)
	preflight, err := alice.PreflightSessionAdoption(context.Background(), string(eligible.ID), bindings)
	if err != nil || !preflight.Eligible || preflight.Bindings != bindings {
		t.Fatalf("eligible preflight = %+v, %v", preflight, err)
	}

	running := saveAdoptionSeamLegacy(t, store, "running", session.StateRunning)
	ineligible, err := alice.PreflightSessionAdoption(context.Background(), string(running.ID), bindings)
	if err != nil || ineligible.Eligible || ineligible.Reason != CapabilityReasonAdoptionActive {
		t.Fatalf("ineligible preflight = %+v, %v", ineligible, err)
	}

	_, foreignErr := bob.PreflightSessionAdoption(context.Background(), string(eligible.ID), bindings)
	_, absentErr := bob.PreflightSessionAdoption(context.Background(), "absent", bindings)
	if foreignErr == nil || absentErr == nil || foreignErr.Error() != absentErr.Error() {
		t.Fatalf("cross-caller oracle: foreign=%v absent=%v", foreignErr, absentErr)
	}

	stale, err := store.Load(context.Background(), eligible.ID)
	if err != nil || stale.Reopen() != nil || stale.BeginTurn() != nil || store.Save(context.Background(), stale) != nil {
		t.Fatalf("make preflight stale: %v", err)
	}
	if _, err := alice.AdoptSession(context.Background(), string(eligible.ID), "stale-key", bindings); err == nil {
		t.Fatal("stale preflight adoption succeeded")
	}

	cancelSource := saveAdoptionSeamLegacy(t, store, "cancel", session.StateCompleted)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := alice.AdoptSession(cancelled, string(cancelSource.ID), "cancel-key", bindings); status.Code(err) != codes.Canceled {
		t.Fatalf("cancelled adoption = %v", err)
	}

	successSource := saveAdoptionSeamLegacy(t, store, "success", session.StateCompleted)
	result, err := alice.AdoptSession(context.Background(), string(successSource.ID), "success-key", bindings)
	if err != nil || result.SessionID == "" || result.SourceSessionID != string(successSource.ID) {
		t.Fatalf("successful adoption = %+v, %v", result, err)
	}
	sourceAfter, sourceErr := store.Load(context.Background(), successSource.ID)
	targetAfter, targetErr := store.Load(context.Background(), session.SessionID(result.SessionID))
	if sourceErr != nil || targetErr != nil || sourceAfter.Kind != session.SessionKindUnknown || targetAfter.Kind != session.SessionKindMain || targetAfter.Workspace != bindings.Workspace {
		t.Fatalf("authoritative source/target: source=%+v/%v target=%+v/%v", sourceAfter, sourceErr, targetAfter, targetErr)
	}
}
