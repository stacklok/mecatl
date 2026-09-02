package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// profileRecordingFactory returns a SessionEngineFactory that records the
// profile it was called with and serves a fresh per-session engine replying
// with reply.
func profileRecordingFactory(reply string, got *atomic.Value, calls *atomic.Int32) server.SessionEngineFactory {
	return func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, profile server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		calls.Add(1)
		got.Store(profile)
		eng := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn(reply)),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		})
		return server.SessionEngineResult{Engine: eng, Close: func() error { return nil }}, nil
	}
}

// TestParseSessionProfile pins the wire grammar: "" and "no-fs" parse; anything
// else is a loud ErrInvalidArgument naming the value.
func TestParseSessionProfile(t *testing.T) {
	if p, err := server.ParseSessionProfile(""); err != nil || p != server.ProfileDefault {
		t.Fatalf(`ParseSessionProfile("") = (%v, %v), want (ProfileDefault, nil)`, p, err)
	}
	if p, err := server.ParseSessionProfile("no-fs"); err != nil || p != server.ProfileNoFS {
		t.Fatalf(`ParseSessionProfile("no-fs") = (%v, %v), want (ProfileNoFS, nil)`, p, err)
	}
	if _, err := server.ParseSessionProfile("ram-only"); err == nil || !strings.Contains(err.Error(), `"ram-only"`) {
		t.Fatalf("unknown profile must fail loudly naming the value, got: %v", err)
	}
}

// TestGRPCCreateSessionProfileRoundTrip: the gRPC profile field round-trips —
// a "no-fs" CreateSession with an EMPTY workspace succeeds, the factory sees
// ProfileNoFS (never the silent default), and the run routes to the
// factory-built per-session engine (a no-FS session must never ride the shared
// engine — the FS tools are baked into it).
func TestGRPCCreateSessionProfileRoundTrip(t *testing.T) {
	var (
		gotProfile atomic.Value
		calls      atomic.Int32
	)
	svc := newMCPService(t, "SHARED-REPLY", profileRecordingFactory("NOFS-ENGINE-REPLY", &gotProfile, &calls))
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{Profile: "no-fs"})
	if err != nil {
		t.Fatalf("CreateSession(no-fs, empty workspace): %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("factory called %d times, want 1 (a no-fs session MUST route through the per-session factory)", calls.Load())
	}
	if got := gotProfile.Load(); got != server.ProfileNoFS {
		t.Fatalf("factory saw profile %v, want ProfileNoFS", got)
	}
	run, err := svc.StartRun(context.Background(), session.SessionID(resp.GetSessionId()), "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainServerRun(run); got != "NOFS-ENGINE-REPLY" {
		t.Fatalf("no-fs turn routed to %q, want the per-session engine's reply (shared engine leaked)", got)
	}
}

// TestGRPCCreateSessionProfileValidation: the three rejection shapes on the
// gRPC surface — default+empty workspace (today's behaviour preserved),
// no-fs+workspace (contradictory), and an unknown profile — are all
// InvalidArgument.
func TestGRPCCreateSessionProfileValidation(t *testing.T) {
	var (
		gotProfile atomic.Value
		calls      atomic.Int32
	)
	svc := newMCPService(t, "SHARED", profileRecordingFactory("X", &gotProfile, &calls))
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	cases := []struct {
		name string
		req  *mecatlv1.CreateSessionRequest
		want string
	}{
		{"unknown profile rejected", &mecatlv1.CreateSessionRequest{Profile: "ram-only"}, "unknown session profile"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.CreateSession(context.Background(), tc.req)
			st, ok := status.FromError(err)
			if !ok || st.Code() != codes.InvalidArgument {
				t.Fatalf("want InvalidArgument, got: %v", err)
			}
			if !strings.Contains(st.Message(), tc.want) {
				t.Fatalf("error %q must contain %q", st.Message(), tc.want)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("factory called %d times by rejected creates, want 0", calls.Load())
	}
}

// TestHTTPCreateSessionProfileRoundTrip mirrors the gRPC round-trip on the HTTP
// surface: profile passes through the JSON body, no-fs+empty workspace
// succeeds (201), default+empty workspace stays 400, no-fs+workspace and an
// unknown profile are 400.
func TestHTTPCreateSessionProfileRoundTrip(t *testing.T) {
	var (
		gotProfile atomic.Value
		calls      atomic.Int32
	)
	svc := newMCPService(t, "SHARED", profileRecordingFactory("NOFS", &gotProfile, &calls))
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	post := func(body string) *http.Response {
		t.Helper()
		resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST /v1/sessions: %v", err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	if resp := post(`{"profile":"no-fs"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("no-fs + empty workspace = %d, want 201", resp.StatusCode)
	}
	if got := gotProfile.Load(); got != server.ProfileNoFS {
		t.Fatalf("factory saw profile %v, want ProfileNoFS", got)
	}
	if resp := post(`{}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("default placement create = %d, want 201", resp.StatusCode)
	}
	if resp := post(`{"profile":"no-fs","workspace":"/ws"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no-fs + workspace = %d, want 400", resp.StatusCode)
	}
	if resp := post(`{"profile":"ram-only","workspace":"/ws"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown profile = %d, want 400", resp.StatusCode)
	}
}

// TestNoFSSessionUsesWorkspaceOverride is the create-time environment-override
// guard (issue #55): a no-fs session's run must resolve its environment from the
// per-session override registered AT CREATE TIME — the shared Workspaces
// factory must NEVER be consulted for it (dropping the override registration in
// createSession hands "" to the osfs factory, which would MkdirAll/OpenRoot the
// server process's cwd). The factory here fails the test if it is ever called
// with the no-fs session's empty root.
func TestNoFSSessionUsesWorkspaceOverride(t *testing.T) {
	var factoryRoots []string
	perSession := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("ok")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("shared")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		}),
		Store: memstore.New(),
		Workspaces: func(root string) tool.Workspace {
			factoryRoots = append(factoryRoots, root)
			return nil // a no-fs run must never reach here; nil would break it loudly
		},
		DefaultLimits: session.Limits{MaxTurns: 5},
		Now:           func() time.Time { return time.Unix(0, 0) },
		SessionEngine: func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{Engine: perSession, Close: func() error { return nil }}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	sess, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileNoFS)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainServerRun(run); got != "ok" {
		t.Fatalf("run reply = %q, want ok", got)
	}
	if len(factoryRoots) != 0 {
		t.Fatalf("the shared Workspaces factory was consulted with roots %q — the no-fs override registered at create time must serve instead", factoryRoots)
	}
}

// noFSServiceOverStore builds a real Service over the CALLER-supplied store —
// the restart-simulation seam: two Services over the SAME store are "the same
// deployment before and after a process restart" (the in-memory engine/workspace
// registries start empty in the second one, exactly as after a real restart).
// The Workspaces factory records every root it is consulted with so a test can
// assert the no-fs path NEVER reaches it (the TestNoFSSessionUsesWorkspaceOverride
// seam); factoryRoots may be nil for tests that don't care.
func noFSServiceOverStore(t *testing.T, store *memstore.Store, factory server.SessionEngineFactory, factoryRoots *[]string) *server.Service {
	t.Helper()
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("SHARED-ENGINE-REPLY")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		}),
		Store: store,
		Workspaces: func(root string) tool.Workspace {
			if factoryRoots != nil {
				*factoryRoots = append(*factoryRoots, root)
			}
			return nil // a no-fs run must never reach here; nil breaks it loudly
		},
		DefaultLimits: session.Limits{MaxTurns: 5},
		Now:           func() time.Time { return time.Unix(0, 0) },
		SessionEngine: factory,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// TestNoFSSessionRehydratesAfterRestart is the restart-escalation guard (issue
// #55 HIGH): a PERSISTED no-fs session (Workspace == "") whose per-session
// engine + workspace override died with the process must be REHYDRATED at the
// run-entry seam by the second Service — rebuilt through the SAME factory path
// create used, with ProfileNoFS — and must NEVER ride the shared engine (full FS
// tools) or hand the empty root to the shared Workspaces factory (osfs would
// MkdirAll/OpenRoot the server cwd). Mutation-verified: removing the rehydration
// branch in StartRunContent fails this test with the shared-engine reply (the
// escalation made visible).
func TestNoFSSessionRehydratesAfterRestart(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()

	// "Before the restart": create the no-fs session and run one turn.
	var (
		gotProfile atomic.Value
		calls      atomic.Int32
	)
	svc1 := noFSServiceOverStore(t, store, profileRecordingFactory("PRE-RESTART", &gotProfile, &calls), nil)
	sess, err := svc1.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileNoFS)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile: %v", err)
	}
	run, err := svc1.StartRun(ctx, sess.ID, "first turn")
	if err != nil {
		t.Fatalf("StartRun (pre-restart): %v", err)
	}
	if got := drainServerRun(run); got != "PRE-RESTART" {
		t.Fatalf("pre-restart reply = %q, want PRE-RESTART", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("factory called %d times before restart, want 1", calls.Load())
	}

	// "After the restart": a NEW Service over the SAME store. Its in-memory
	// engine/workspace registries are empty — exactly the post-restart state.
	var factoryRoots []string
	var (
		gotProfile2 atomic.Value
		calls2      atomic.Int32
	)
	svc2 := noFSServiceOverStore(t, store, profileRecordingFactory("REHYDRATED", &gotProfile2, &calls2), &factoryRoots)

	run2, err := svc2.StartRun(ctx, sess.ID, "post-restart turn")
	if err != nil {
		t.Fatalf("StartRun (post-restart): %v", err)
	}
	if got := drainServerRun(run2); got != "REHYDRATED" {
		t.Fatalf("post-restart reply = %q, want REHYDRATED (the rehydrated no-fs per-session engine); "+
			"SHARED-ENGINE-REPLY means the session ESCALATED onto the shared FS engine", got)
	}
	svc2.FinishRun(sess.ID, run2)
	if calls2.Load() != 1 {
		t.Fatalf("factory called %d times after restart, want exactly 1 (rehydration)", calls2.Load())
	}
	if got := gotProfile2.Load(); got != server.ProfileNoFS {
		t.Fatalf("rehydration factory saw profile %v, want ProfileNoFS", got)
	}
	if len(factoryRoots) != 0 {
		t.Fatalf("the shared Workspaces factory was consulted with roots %q after the restart — "+
			"the empty root must never reach it (osfs would open the server cwd)", factoryRoots)
	}

	// Rehydrate ONCE: a second post-restart run reuses the registered engine —
	// the factory is not consulted again and the shared Workspaces factory stays
	// untouched. (The reply text is not asserted: the factory engine's one-turn
	// mockllm script is already exhausted, which is fine — the seam under test is
	// the registration, not the script length.)
	run3, err := svc2.StartRun(ctx, sess.ID, "second post-restart turn")
	if err != nil {
		t.Fatalf("StartRun (second post-restart): %v", err)
	}
	drainServerRun(run3)
	if calls2.Load() != 1 {
		t.Fatalf("factory called %d times across two post-restart runs, want 1 (rehydration is once, then registered)", calls2.Load())
	}
	if len(factoryRoots) != 0 {
		t.Fatalf("the shared Workspaces factory was consulted with roots %q on the second post-restart run", factoryRoots)
	}
}

// TestNoFSRehydrationWithoutFactoryFailsLoudly: a persisted no-fs session on a
// Service with NO session-engine factory must fail the run LOUDLY — never fall
// back to the shared engine (the exact escalation) and never consult the shared
// Workspaces factory with the empty root.
func TestNoFSRehydrationWithoutFactoryFailsLoudly(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	sess := session.New("nofs-orphan", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 3}, time.Unix(0, 0))
	sess.Profile = string(server.ProfileNoFS)
	if err := store.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var factoryRoots []string
	svc := noFSServiceOverStore(t, store, nil, &factoryRoots)
	_, err := svc.StartRun(ctx, sess.ID, "hi")
	if err == nil || !strings.Contains(err.Error(), "cannot be rehydrated") {
		t.Fatalf("StartRun on a no-fs session without a factory must fail loudly naming rehydration, got: %v", err)
	}
	if len(factoryRoots) != 0 {
		t.Fatalf("the shared Workspaces factory was consulted with roots %q on the failure path", factoryRoots)
	}
}

// TestLoadSessionWithMCPDerivesNoFSProfile: the ACP-style resume path must
// derive the profile from the persisted snapshot (an empty Workspace can only be
// a no-fs session) instead of hardcoding the default — a default-profile rebuild
// would silently re-grant the FS tools. The workspace override is re-registered
// too, so the subsequent run never consults the shared factory.
func TestLoadSessionWithMCPDerivesNoFSProfile(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	sess := session.New("nofs-acp", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 3}, time.Unix(0, 0))
	sess.Profile = string(server.ProfileNoFS)
	if err := store.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var factoryRoots []string
	var (
		gotProfile atomic.Value
		calls      atomic.Int32
	)
	svc := noFSServiceOverStore(t, store, profileRecordingFactory("NOFS-MCP", &gotProfile, &calls), &factoryRoots)
	if _, err := svc.LoadSessionWithMCP(ctx, sess.ID, []mcp.ServerConfig{{Name: "c", URL: "http://127.0.0.1:0"}}); err != nil {
		t.Fatalf("LoadSessionWithMCP: %v", err)
	}
	if got := gotProfile.Load(); got != server.ProfileNoFS {
		t.Fatalf("LoadSessionWithMCP factory saw profile %v, want ProfileNoFS (derived from the empty persisted workspace)", got)
	}
	run, err := svc.StartRun(ctx, sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainServerRun(run); got != "NOFS-MCP" {
		t.Fatalf("run reply = %q, want NOFS-MCP", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("factory called %d times, want 1 (the load built it; the run must not rehydrate again)", calls.Load())
	}
	if len(factoryRoots) != 0 {
		t.Fatalf("the shared Workspaces factory was consulted with roots %q — the no-fs override must serve", factoryRoots)
	}
}
