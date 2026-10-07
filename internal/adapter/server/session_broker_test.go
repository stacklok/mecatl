package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/redis/go-redis/v9"
	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/adapter/localauthority"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type sessionBrokerFailStore struct {
	*memstore.Store
	fail      atomic.Bool
	ambiguous atomic.Bool
	phase     atomic.Value
}

func (s *sessionBrokerFailStore) Save(ctx context.Context, sess *session.Session) error {
	phase, _ := s.phase.Load().(string)
	a, _ := sess.BrokerAccess()
	phaseFailure := phase != "" && a.Current != nil && a.Current.Phase == phase
	if s.fail.Load() || phaseFailure {
		if s.ambiguous.Load() {
			if err := s.Store.Save(ctx, sess); err != nil {
				return err
			}
		}
		return errors.New("injected save failure")
	}
	return s.Store.Save(ctx, sess)
}

func sessionBrokerRPCClient(t *testing.T, api c.SessionService, owner *session.Principal) *mcpbrokergrpc.SessionClient {
	t.Helper()
	rpc, err := mcpbrokergrpc.NewSessionRPC(api)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		return h(session.WithPrincipal(ctx, owner), req)
	}))
	p.RegisterSessionServiceServer(server, rpc)
	listener := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(listener) }()
	client, err := mcpbrokergrpc.NewSessionClient("passthrough:///host-broker", time.Second, 3*time.Second, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(); server.Stop(); _ = listener.Close() })
	return client
}

func sessionBrokerTestHost(t *testing.T, client mcpbrokergrpc.SessionHostClient, store port.SessionStore, provider port.LLMProvider, onBuild func([]tool.Tool)) *Service {
	t.Helper()
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store, PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test", SessionBroker: client, WorkspaceEnrollment: true,
		NewID:         func() session.SessionID { return "broker-session-host" },
		RootAuthority: func(session.SessionKind) session.Authority { return session.Authority{Provenance: "test"} },
		SessionEngineWithTools: func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, tools []tool.Tool) (SessionEngineResult, error) {
			if onBuild != nil {
				onBuild(tools)
			}
			cat := tool.NewCatalog()
			for _, candidate := range tools {
				if err := cat.Register(candidate); err != nil {
					t.Fatal(err)
				}
			}
			return SessionEngineResult{Engine: agent.NewEngine(agent.Deps{LLM: provider, Catalog: cat, Store: store, AuthorityEvaluator: localauthority.New(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)}), Close: func() error { return nil }}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

type sessionBrokerCleanupClient struct {
	mcpbrokergrpc.SessionHostClient
	fail    bool
	changed bool
	begins  int
}

func (client *sessionBrokerCleanupClient) DisconnectTools(ctx context.Context, ref c.SessionRef, cat c.ConnectionRef) (c.DisconnectResult, error) {
	if client.fail {
		return 0, errors.New("injected cleanup failure")
	}
	if client.changed {
		return c.ConnectionChanged, nil
	}
	return client.SessionHostClient.DisconnectTools(ctx, ref, cat)
}
func (client *sessionBrokerCleanupClient) CancelEnrollment(ctx context.Context, ref c.SessionRef, enrollment c.EnrollmentRef) (c.CancelResult, error) {
	if client.fail {
		return 0, errors.New("injected enrollment cleanup failure")
	}
	return client.SessionHostClient.CancelEnrollment(ctx, ref, enrollment)
}
func (client *sessionBrokerCleanupClient) BeginEnrollment(ctx context.Context, ref c.SessionRef) (c.BeginEnrollmentOutcome, error) {
	client.begins++
	return client.SessionHostClient.BeginEnrollment(ctx, ref)
}

func TestSessionBrokerHostNativeAnonymousSaveRestoreAndFence(t *testing.T) {
	testSessionBrokerHostNativeOccurrences(t, 1)
}

func TestBrokerPathRetirement_FollowupHostOccurrence(t *testing.T) {
	testSessionBrokerHostNativeOccurrences(t, 70)
}

func testSessionBrokerHostNativeOccurrences(t *testing.T, count int) {
	t.Helper()
	var calls atomic.Int32
	store := &sessionBrokerFailStore{Store: memstore.New()}
	upstream := sdk.NewServer(&sdk.Implementation{Name: "host-proof", Version: "test"}, nil)
	sdk.AddTool(upstream, &sdk.Tool{Name: "echo"}, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
		sequence := calls.Add(1)
		durable, err := store.Load(ctx, "broker-session-host")
		if err != nil {
			t.Error(err)
			return nil, nil, err
		}
		a, _ := durable.BrokerAccess()
		if a.Current == nil || a.Current.Phase != "dispatched" || a.Current.Attempt.Slot != 0 || a.Current.Attempt.Sequence != uint64(sequence) {
			t.Errorf("upstream invoked before exact durable dispatch: %+v", a.Current)
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "native"}}}, nil, nil
	})
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return upstream }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer httpServer.Close()
	process, err := mcpbroker.NewToolHiveProcess(t.Context(), mcpbroker.ToolHiveConfig{DeferAnonymousDiscovery: true, Profiles: []mcpbroker.ToolHiveProfile{{Name: "echo", URL: httpServer.URL, Auth: "none"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	db := miniredis.RunT(t)
	storage := redis.NewClient(&redis.Options{Addr: db.Addr()})
	defer storage.Close()
	api, err := mcpbroker.NewSessionAPI(process, storage, func(context.Context) *session.Principal {
		return &session.Principal{Issuer: "https://workload.test", Subject: "host"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	client := sessionBrokerRPCClient(t, api, &session.Principal{Issuer: "https://owner.test", Subject: "owner"})
	var turns []mockllm.Turn
	repeated := count
	if count > 1 {
		turns = append(turns, mockllm.ToolCallTurn(
			session.NewToolCall("first", "mcp__echo__echo", []byte(`{}`)),
			session.NewToolCall("second", "mcp__echo__echo", []byte(`{}`)),
		))
		repeated -= 2
	}
	for range repeated {
		turns = append(turns, mockllm.ToolCallTurn(session.NewToolCall("once", "mcp__echo__echo", []byte(`{}`))))
	}
	turns = append(turns, mockllm.TextTurn("done"))
	provider := mockllm.New(turns...)
	var advertised int
	svc := sessionBrokerTestHost(t, client, store, provider, func(tools []tool.Tool) { advertised = len(tools) })
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	access, ok := created.BrokerAccess()
	if !ok || created.ExternalBinding != "" {
		t.Fatal("missing stable reference or native binding persisted")
	}
	if _, ok := created.BrokerCredentialCustody(); ok {
		t.Fatal("native custody persisted")
	}
	if advertised != 0 {
		t.Fatal("Open exposed tools")
	}
	store.fail.Store(true)
	if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err == nil {
		t.Fatal("save failure exposed enrollment")
	}
	if entry := svc.sessionEngines[created.ID]; entry != nil && entry.engine.HasTool("mcp__echo__echo") {
		t.Fatal("failed save registered executable catalogue")
	}
	store.fail.Store(false)
	if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	adopted, _ := saved.BrokerAccess()
	if adopted.Session != access.Session || adopted.Catalogue == access.Catalogue {
		t.Fatal("stable ref/catalogue adoption")
	}
	// A second host opens the exact durable ref and saves adoption before build.
	svc.Close()
	checkingRestore := true
	second := sessionBrokerTestHost(t, client, store, provider, func(tools []tool.Tool) {
		if len(tools) == 0 || !checkingRestore {
			return
		}
		durable, err := store.Load(t.Context(), created.ID)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := durable.BrokerAccess()
		r, cat := tools[0].(tool.DurableBrokerInvocation).BrokerInvocationRefs()
		if a.Session != r || a.Catalogue != cat {
			t.Fatal("advertised before durable adoption")
		}
	})
	store.fail.Store(true)
	if _, err := second.LoadSession(t.Context(), created.ID); err == nil {
		t.Fatal("restore advertised without durable catalogue adoption")
	}
	if entry := second.sessionEngines[created.ID]; entry != nil && entry.engine.HasTool("mcp__echo__echo") {
		t.Fatal("restore save failure registered tools")
	}
	store.fail.Store(false)
	if _, err := second.LoadSession(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	run, err := second.StartRunContent(t.Context(), created.ID, "execute", nil)
	if err != nil {
		t.Fatal(err)
	}
	for ev := range run.Events() {
		if ev.ToolResult != nil {
			t.Logf("result: %+v", *ev.ToolResult)
		}
	}
	second.FinishRun(created.ID, run)
	if calls.Load() != int32(count) {
		t.Fatalf("native calls=%d", calls.Load())
	}
	saved, err = store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	fenced, _ := saved.BrokerAccess()
	if fenced.Current == nil || fenced.Current.CallID != "once" || fenced.Current.Disposition != session.BrokerAttemptCompleted || fenced.AdmittedSequence != uint64(count) {
		t.Fatalf("missing crash fence: %+v current=%+v", fenced, fenced.Current)
	}
	if _, err := saved.PrepareBrokerInvocation(fenced.Session, fenced.Catalogue, session.NewToolCall("once", "mcp__echo__echo", []byte(`{}`)), time.Now()); err != nil {
		t.Fatal("durably paired occurrence prevented provider ID reuse")
	}
	cleanup := &sessionBrokerCleanupClient{SessionHostClient: client, fail: true}
	second.cfg.SessionBroker = cleanup
	if err := second.DisconnectWorkspaceServices(t.Context(), created.ID); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	saved, _ = store.Load(t.Context(), created.ID)
	withdrawn, _ := saved.BrokerAccess()
	if !withdrawn.Withdrawn {
		t.Fatal("withdrawal not durable")
	}
	// Restore must remain withdrawn; only an explicit Connect may record intent.
	second.closeSessionLocal(created.ID)
	if _, err := second.LoadSession(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	saved, _ = store.Load(t.Context(), created.ID)
	if a, _ := saved.BrokerAccess(); !a.Withdrawn || len(saved.Authority.CapabilitySet.Tools) != 0 {
		t.Fatal("restore reconstructed withdrawn authority")
	}
	checkingRestore = false
	if _, err := second.ConnectWorkspaceServices(t.Context(), created.ID); err == nil || cleanup.begins != 0 {
		t.Fatal("reconnect bypassed failed cleanup")
	}
	cleanup.fail, cleanup.changed = false, true
	if _, err := second.ConnectWorkspaceServices(t.Context(), created.ID); err == nil || cleanup.begins != 0 {
		t.Fatal("reconnect bypassed old-revision fence")
	}
	cleanup.changed = false
	store.fail.Store(true)
	if _, err := second.ConnectWorkspaceServices(t.Context(), created.ID); err == nil {
		t.Fatal("Begin admitted before durable reconnect intent")
	}
	store.fail.Store(false)
	connected, err := second.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || connected.Status != c.WorkspaceEnrollmentConnected {
		t.Fatalf("explicit reconnect: %+v %v", connected, err)
	}
	saved, _ = store.Load(t.Context(), created.ID)
	reconnected, _ := saved.BrokerAccess()
	if reconnected.Withdrawn || reconnected.Session != withdrawn.Session || reconnected.Catalogue == withdrawn.Catalogue || reconnected.AdmittedSequence != withdrawn.AdmittedSequence {
		t.Fatal("reconnect lost exact identity/revision or replay fence")
	}
	if _, err := saved.PrepareBrokerInvocation(withdrawn.Session, withdrawn.Catalogue, session.NewToolCall("stale", "mcp__echo__echo", []byte(`{}`)), time.Now()); err == nil {
		t.Fatal("old revision admitted after reconnect")
	}
	if err := second.DeleteSession(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.OpenSession(t.Context(), &access.Session); err == nil {
		t.Fatal("deleted reference reopened")
	}
}

func TestSessionBrokerHostPendingDisconnectSafeIntent(t *testing.T) {
	f := newContinuityFixture(t)
	storage := redis.NewClient(&redis.Options{Addr: f.redis.Addr()})
	defer storage.Close()
	api, err := mcpbroker.NewSessionAPI(f.process, storage, func(context.Context) *session.Principal { return f.workload.Clone() })
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	client := &sessionBrokerCleanupClient{SessionHostClient: sessionBrokerRPCClient(t, api, session.PrincipalFromContext(f.owner))}
	store := &sessionBrokerFailStore{Store: memstore.New()}
	svc := sessionBrokerTestHost(t, client, store, mockllm.New(mockllm.TextTurn("done")), nil)
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || pending.Status != c.WorkspaceEnrollmentPending {
		t.Fatalf("pending: %+v %v", pending, err)
	}
	store.fail.Store(true)
	if err := svc.DisconnectWorkspaceServices(t.Context(), created.ID); err == nil {
		t.Fatal("withdrawal save succeeded")
	}
	saved, _ := store.Load(t.Context(), created.ID)
	access, _ := saved.BrokerAccess()
	if _, ok := saved.PendingWorkspaceEnrollment(); !ok || access.Withdrawn {
		t.Fatal("failed intent save cleared durable pending flow")
	}
	store.fail.Store(false)
	client.fail = true
	if err := svc.DisconnectWorkspaceServices(t.Context(), created.ID); err == nil {
		t.Fatal("cleanup succeeded")
	}
	saved, _ = store.Load(t.Context(), created.ID)
	access, _ = saved.BrokerAccess()
	if _, ok := saved.PendingWorkspaceEnrollment(); !ok || !access.Withdrawn {
		t.Fatal("failed cancellation lost durable pending cleanup intent")
	}
	begins := client.begins
	if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err == nil || client.begins != begins {
		t.Fatal("reconnect bypassed unresolved cleanup")
	}
	client.fail = false
	fresh, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || fresh.Status != c.WorkspaceEnrollmentPending || fresh.Ref.ID == pending.Ref.ID || fresh.URL == pending.URL {
		t.Fatalf("fresh pending: %+v %v", fresh, err)
	}
	flow, err := api.ObserveEnrollment(f.owner, c.SessionRef(access.Session), c.EnrollmentRef(pending.Ref.ID))
	if err != nil || flow.Kind == c.FlowCompleted || flow.Kind == c.FlowPending {
		t.Fatalf("old observation: %#v %v", flow, err)
	}
}

func TestSessionBrokerHostNativeProtectedEnrollmentReplacement(t *testing.T) {
	f := newContinuityFixture(t)
	storage := redis.NewClient(&redis.Options{Addr: f.redis.Addr()})
	defer storage.Close()
	api, err := mcpbroker.NewSessionAPI(f.process, storage, func(context.Context) *session.Principal { return f.workload.Clone() })
	if err != nil {
		t.Fatal(err)
	}
	client := sessionBrokerRPCClient(t, api, session.PrincipalFromContext(f.owner))
	store := memstore.New()
	svc := sessionBrokerTestHost(t, client, store, mockllm.New(mockllm.TextTurn("done")), nil)
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	begun, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || begun.Status != c.WorkspaceEnrollmentPending {
		t.Fatalf("begin: %+v %v", begun, err)
	}
	browser := f.gateway.Client()
	browser.Timeout = 8 * time.Second
	response, err := browser.Get(begun.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	connected, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || connected.Status != c.WorkspaceEnrollmentConnected {
		t.Fatalf("connect: %+v %v", connected, err)
	}
	saved, _ := store.Load(t.Context(), created.ID)
	old, _ := saved.BrokerAccess()
	svc.Close()
	_ = api.Close()
	f.replaceBroker()
	replacement, err := mcpbroker.NewSessionAPI(f.process, storage, func(context.Context) *session.Principal { return f.workload.Clone() })
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	freshClient := sessionBrokerRPCClient(t, replacement, session.PrincipalFromContext(f.owner))
	second := sessionBrokerTestHost(t, freshClient, store, mockllm.New(mockllm.TextTurn("done")), nil)
	if _, err := second.LoadSession(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	saved, _ = store.Load(t.Context(), created.ID)
	fresh, _ := saved.BrokerAccess()
	if fresh.Session != old.Session || fresh.Catalogue == old.Catalogue || saved.ExternalBinding != "" {
		t.Fatal("replacement authority/reference adoption failed")
	}
	if _, ok := saved.BrokerCredentialCustody(); ok {
		t.Fatal("host stored native custody")
	}
	if err := second.DisconnectWorkspaceServices(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	begun, err = second.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || begun.Status != c.WorkspaceEnrollmentPending {
		t.Fatalf("explicit protected reconnect: %+v %v", begun, err)
	}
	response, err = browser.Get(begun.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	connected, err = second.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || connected.Status != c.WorkspaceEnrollmentConnected {
		t.Fatalf("protected reconnect completion: %+v %v", connected, err)
	}
	saved, _ = store.Load(t.Context(), created.ID)
	reconnected, _ := saved.BrokerAccess()
	if reconnected.Withdrawn || reconnected.Session != fresh.Session || reconnected.Catalogue == fresh.Catalogue {
		t.Fatal("protected reconnect restored old authority")
	}
}
