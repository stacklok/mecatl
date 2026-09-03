package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type closeSurfaceFixture struct {
	svc       *server.Service
	store     *memstore.Store
	lease     *fakeLease
	closed    atomic.Int32
	sessionID session.SessionID
}

func newCloseSurfaceFixture(t *testing.T, provider port.LLMProvider, policy port.PermissionPolicy, tools ...tool.Tool) *closeSurfaceFixture {
	return newCloseSurfaceFixtureWithLease(t, true, provider, policy, tools...)
}

func newCloseSurfaceFixtureWithLease(t *testing.T, withLease bool, provider port.LLMProvider, policy port.PermissionPolicy, tools ...tool.Tool) *closeSurfaceFixture {
	t.Helper()
	store := memstore.New()
	lease := &fakeLease{}
	fixture := &closeSurfaceFixture{store: store, lease: lease}
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		catalog := tool.NewCatalog()
		for _, candidate := range tools {
			catalog.MustRegister(candidate)
		}
		engine := agent.NewEngine(agent.Deps{LLM: provider, Catalog: catalog, Policy: policy, Store: store, Model: "close-test"})
		return server.SessionEngineResult{Engine: engine, Close: func() error { fixture.closed.Add(1); return nil }}, nil
	}
	shared := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "shared"})
	var sessionLease port.SessionLease
	if withLease {
		sessionLease = lease
	}
	svc, err := server.NewService(server.Config{
		Engine: shared, Store: store,
		PlacementProvider: testPlacementProvider{root: "/ws", firstBind: &atomic.Bool{}},
		PlacementScope:    "test",
		SharedEngineRoot:  "/ws",
		Now:               func() time.Time { return time.Unix(0, 0) }, SessionEngine: factory,
		SessionLease: sessionLease, LeaseOwner: "close-test", LeaseTTL: time.Hour, LeaseRenewInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	fixture.svc = svc
	sess, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{MaxTurns: 3}, server.ProviderSelector{ProviderID: "fixture"}, server.ProfileDefault)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile: %v", err)
	}
	fixture.sessionID = sess.ID
	return fixture
}

type closeSurface func(t *testing.T, svc *server.Service, id session.SessionID) (grpcCode codes.Code, httpCode int)

func grpcCloseSurface(t *testing.T, svc *server.Service, id session.SessionID) (codes.Code, int) {
	t.Helper()
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	_, err := client.CloseSession(context.Background(), &mecatlv1.CloseSessionRequest{SessionId: string(id)})
	return status.Code(err), 0
}

func httpCloseSurface(t *testing.T, svc *server.Service, id session.SessionID) (codes.Code, int) {
	t.Helper()
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/v1/sessions/"+string(id), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE session: %v", err)
	}
	defer resp.Body.Close()
	return codes.OK, resp.StatusCode
}

func TestADR_0293_CloseGRPCAndHTTPRejectLiveOrAwaitingRun(t *testing.T) {
	for surfaceName, close := range map[string]closeSurface{"grpc": grpcCloseSurface, "http": httpCloseSurface} {
		t.Run(surfaceName+"/running", func(t *testing.T) {
			fixture := newCloseSurfaceFixture(t, blockingProvider{}, permpolicy.NewPolicy(allowRules(), nil))
			run, err := fixture.svc.StartRun(context.Background(), fixture.sessionID, "stay live")
			if err != nil {
				t.Fatalf("StartRun: %v", err)
			}
			grpcCode, httpCode := close(t, fixture.svc, fixture.sessionID)
			if surfaceName == "grpc" && grpcCode != codes.FailedPrecondition {
				t.Fatalf("gRPC code = %v, want FailedPrecondition", grpcCode)
			}
			if surfaceName == "http" && httpCode != http.StatusPreconditionFailed {
				t.Fatalf("HTTP status = %d, want 412", httpCode)
			}
			if fixture.closed.Load() != 0 || fixture.lease.releaseCount() != 0 || !fixture.svc.IsLive(fixture.sessionID) {
				t.Fatalf("rejected close changed ownership: engine closes=%d lease releases=%d live=%t", fixture.closed.Load(), fixture.lease.releaseCount(), fixture.svc.IsLive(fixture.sessionID))
			}
			run.Cancel()
			for range run.Events() {
			}
			fixture.svc.FinishRun(fixture.sessionID, run)
			fixture.svc.CloseSession(fixture.sessionID)
		})

		t.Run(surfaceName+"/awaiting", func(t *testing.T) {
			var ran atomic.Int64
			fixture := newCloseSurfaceFixture(t,
				mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call-1", "Write", json.RawMessage(`{}`)))),
				permpolicy.NewPolicy(nil, nil), &writeAskTool{ran: &ran})
			run, err := fixture.svc.StartRun(context.Background(), fixture.sessionID, "ask")
			if err != nil {
				t.Fatalf("StartRun: %v", err)
			}
			var ask session.PendingAsk
			for ev := range run.Events() {
				if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
					ask = *ev.Ask
					break
				}
			}
			if ask.AskID == "" {
				t.Fatal("run did not reach awaiting")
			}
			fixture.svc.Persist(context.Background(), fixture.sessionID)
			grpcCode, httpCode := close(t, fixture.svc, fixture.sessionID)
			if surfaceName == "grpc" && grpcCode != codes.FailedPrecondition {
				t.Fatalf("gRPC code = %v, want FailedPrecondition", grpcCode)
			}
			if surfaceName == "http" && httpCode != http.StatusPreconditionFailed {
				t.Fatalf("HTTP status = %d, want 412", httpCode)
			}
			if fixture.closed.Load() != 0 || fixture.lease.releaseCount() != 0 || !fixture.svc.IsLive(fixture.sessionID) {
				t.Fatalf("rejected close changed ownership: engine closes=%d lease releases=%d live=%t", fixture.closed.Load(), fixture.lease.releaseCount(), fixture.svc.IsLive(fixture.sessionID))
			}
			if err := fixture.svc.Approve(context.Background(), fixture.sessionID, ask.AskID, session.VerdictDeny); err != nil {
				t.Fatalf("deny cleanup: %v", err)
			}
			for range run.Events() {
			}
			fixture.svc.FinishRun(fixture.sessionID, run)
			fixture.svc.CloseSession(fixture.sessionID)
		})
	}
}

func TestADR_0293_CloseGRPCAndHTTPPreservePersistedAwaitingResumePoint(t *testing.T) {
	cases := map[string]struct {
		transport string
		close     closeSurface
		leased    bool
	}{
		"grpc/leased":   {"grpc", grpcCloseSurface, true},
		"grpc/no-lease": {"grpc", grpcCloseSurface, false},
		"http/leased":   {"http", httpCloseSurface, true},
		"http/no-lease": {"http", httpCloseSurface, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newCloseSurfaceFixtureWithLease(t, tc.leased, mockllm.New(mockllm.TextTurn("done")), permpolicy.NewPolicy(allowRules(), nil))
			run, err := fixture.svc.StartRun(context.Background(), fixture.sessionID, "acquire ownership")
			if err != nil {
				t.Fatalf("StartRun: %v", err)
			}
			for range run.Events() {
			}
			fixture.svc.FinishRun(fixture.sessionID, run)

			persisted := session.New(fixture.sessionID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 3}, time.Unix(0, 0))
			if err := persisted.RecordUserPrompt("durable request", nil); err != nil {
				t.Fatal(err)
			}
			if err := persisted.BeginTurn(); err != nil {
				t.Fatal(err)
			}
			call := session.NewToolCall("durable-call", "Write", json.RawMessage(`{"path":"f.go"}`))
			if err := persisted.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
				t.Fatal(err)
			}
			wantAsk := session.PendingAsk{AskID: "durable-ask", Tool: "Write", Call: call.ID}
			if err := persisted.PauseForApproval(wantAsk); err != nil {
				t.Fatal(err)
			}
			if err := fixture.store.Save(context.Background(), persisted); err != nil {
				t.Fatalf("Save awaiting: %v", err)
			}

			grpcCode, httpCode := tc.close(t, fixture.svc, fixture.sessionID)
			if tc.transport == "grpc" && grpcCode != codes.OK {
				t.Fatalf("gRPC code = %v, want OK", grpcCode)
			}
			if tc.transport == "http" && httpCode != http.StatusNoContent {
				t.Fatalf("HTTP status = %d, want 204", httpCode)
			}
			wantReleases := 0
			if tc.leased {
				wantReleases = 1
			}
			if fixture.closed.Load() != 1 || fixture.lease.releaseCount() != wantReleases || fixture.svc.IsLive(fixture.sessionID) {
				t.Fatalf("close did not release local ownership: engine closes=%d lease releases=%d (want %d) live=%t", fixture.closed.Load(), fixture.lease.releaseCount(), wantReleases, fixture.svc.IsLive(fixture.sessionID))
			}
			got, err := fixture.store.Load(context.Background(), fixture.sessionID)
			if err != nil {
				t.Fatalf("Load after close: %v", err)
			}
			gotAsk, ok := got.PendingAsk()
			if got.State != session.StateAwaiting || !ok || !reflect.DeepEqual(gotAsk, wantAsk) {
				t.Fatalf("durable resume point changed: state=%q ask=%+v ok=%t, want awaiting %+v", got.State, gotAsk, ok, wantAsk)
			}
		})
	}
}
