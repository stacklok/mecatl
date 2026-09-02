package server_test

import (
	"context"
	"errors"
	"fmt"
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

// TestCreateSessionDefaultSelectorUsesSharedEngine: the zero selector + no specs
// builds NO per-session engine (the fast path), and the turn routes to the shared
// engine.
func TestCreateSessionDefaultSelectorUsesSharedEngine(t *testing.T) {
	var factoryCalls atomic.Int32
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		factoryCalls.Add(1)
		return server.SessionEngineResult{}, errors.New("factory must not be called for the zero selector")
	}
	svc := newMCPService(t, "SHARED-REPLY", factory)

	sess, err := svc.CreateSessionWithProvider(context.Background(), "/ws", session.ModeDefault, session.Limits{}, server.ProviderSelector{})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider(zero): %v", err)
	}
	if factoryCalls.Load() != 0 {
		t.Fatalf("factory called %d times for the zero selector, want 0 (shared-engine fast path)", factoryCalls.Load())
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainServerRun(run); got != "SHARED-REPLY" {
		t.Fatalf("zero-selector turn routed to %q, want the shared engine's reply", got)
	}
}

// TestCreateSessionModelSelectorRegistersPerSessionEngine: a non-empty selector
// builds a per-session engine, StartRun routes to it, and CloseSession evicts it +
// calls the close func.
func TestCreateSessionModelSelectorRegistersPerSessionEngine(t *testing.T) {
	var closed atomic.Int32
	perSession := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("PER-SESSION-REPLY")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	var gotSel server.ProviderSelector
	factory := func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		gotSel = sel
		return server.SessionEngineResult{Engine: perSession, Close: func() error { closed.Add(1); return nil }}, nil
	}
	svc := newMCPService(t, "SHARED-REPLY", factory)

	sel := server.ProviderSelector{ProviderID: "openrouter", ModelID: "anthropic/claude-opus-4.5"}
	sess, err := svc.CreateSessionWithProvider(context.Background(), "/ws", session.ModeDefault, session.Limits{}, sel)
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	if gotSel != sel {
		t.Fatalf("factory got selector %+v, want %+v", gotSel, sel)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainServerRun(run); got != "PER-SESSION-REPLY" {
		t.Fatalf("turn routed to %q, want the per-session engine's reply", got)
	}
	svc.CloseSession(sess.ID)
	if closed.Load() != 1 {
		t.Fatalf("close func ran %d times, want 1 (CloseSession must tear the per-session engine down)", closed.Load())
	}
}

// TestCreateSessionUnknownProviderInvalidArgument: a factory that rejects an
// unknown provider with ErrInvalidArgument surfaces as codes.InvalidArgument via
// the gRPC handler — NOT a silent default.
func TestCreateSessionUnknownProviderInvalidArgument(t *testing.T) {
	factory := func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		return server.SessionEngineResult{}, fmt.Errorf("%w: unknown or unavailable provider %q", server.ErrInvalidArgument, sel.ProviderID)
	}
	svc := newMCPService(t, "shared", factory)

	// Service-level: the error wraps ErrInvalidArgument.
	_, err := svc.CreateSessionWithProvider(context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "nope"})
	if !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("service error = %v, want it to wrap ErrInvalidArgument", err)
	}

	// gRPC-level: codes.InvalidArgument via toStatus.
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	_, gerr := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
		ProviderId: "nope",
	})
	if status.Code(gerr) != codes.InvalidArgument {
		t.Fatalf("gRPC CreateSession unknown-provider code = %v, want InvalidArgument", status.Code(gerr))
	}
}

// TestCreateSessionModelWithoutProviderInvalidArgument: model_id without
// provider_id is rejected at the create boundary (a bare model on the default
// provider is ambiguous) — the factory is never consulted.
func TestCreateSessionModelWithoutProviderInvalidArgument(t *testing.T) {
	var factoryCalls atomic.Int32
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		factoryCalls.Add(1)
		return server.SessionEngineResult{}, nil
	}
	svc := newMCPService(t, "shared", factory)

	_, err := svc.CreateSessionWithProvider(context.Background(), "/ws", session.ModeDefault, session.Limits{},
		server.ProviderSelector{ModelID: "gpt-x"})
	if !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument for model_id without provider_id", err)
	}
	if factoryCalls.Load() != 0 {
		t.Fatalf("factory called %d times, want 0 (rejected before the factory)", factoryCalls.Load())
	}

	// gRPC surface maps it to InvalidArgument too.
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	_, gerr := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
		ModelId: "gpt-x",
	})
	if status.Code(gerr) != codes.InvalidArgument {
		t.Fatalf("gRPC code = %v, want InvalidArgument", status.Code(gerr))
	}
}

// drainServerRun consumes a server-started run to completion, auto-allowing any
// permission ask, and returns the terminal result text.
func drainServerRun(run *agent.Run) string {
	var final string
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			final = ev.Result.Text
		}
	}
	return final
}

// TestGRPCCreateSessionNoSelectorOldClientContract: a gRPC CreateSession with NO
// selector (Workspace only — exactly what an OLD client that predates the
// provider_id/model_id fields sends) succeeds, builds NO per-session engine, and a
// turn routes to the shared default engine. Locks the additive-proto old-client
// contract over the wire.
func TestGRPCCreateSessionNoSelectorOldClientContract(t *testing.T) {
	var factoryCalls atomic.Int32
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		factoryCalls.Add(1)
		return server.SessionEngineResult{}, errors.New("factory must not be called for a no-selector old-client create")
	}
	svc := newMCPService(t, "SHARED-REPLY", factory)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	// Old-client shape: no provider_id / model_id on the request.
	resp, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession(no selector): %v", err)
	}
	if resp.GetSessionId() == "" {
		t.Fatal("CreateSession returned an empty session id")
	}
	if factoryCalls.Load() != 0 {
		t.Fatalf("factory called %d times for a no-selector create, want 0 (shared engine)", factoryCalls.Load())
	}
	// The shared engine drives the turn (proves the no-selector session uses it).
	run, err := svc.StartRun(context.Background(), session.SessionID(resp.GetSessionId()), "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainServerRun(run); got != "SHARED-REPLY" {
		t.Fatalf("no-selector turn routed to %q, want the shared engine's reply", got)
	}
}

// cappedSelectorService builds a Service whose per-session-engine factory always
// succeeds (returning a stub engine + counting close), with MaxSessionEngines set to
// limit, so the cap can be exercised without real MCP/providers.
func cappedSelectorService(t *testing.T, limit int, closed *atomic.Int32) *server.Service {
	t.Helper()
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("shared")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		eng := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("per-session")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "per-session-model",
		})
		return server.SessionEngineResult{Engine: eng, Close: func() error { closed.Add(1); return nil }}, nil
	}
	svc, err := server.NewService(server.Config{
		Engine:            shared,
		Store:             memstore.New(),
		Workspaces:        func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		DefaultLimits:     session.Limits{MaxTurns: 10},
		Now:               func() time.Time { return time.Unix(0, 0) },
		SessionEngine:     factory,
		MaxSessionEngines: limit,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestCreateSessionEnginesCapped: with MaxSessionEngines=N, creating N selector
// sessions without EndSession succeeds; the (N+1)th returns ErrTooManySessionEngines
// (gRPC ResourceExhausted); EndSession frees a slot so a subsequent create succeeds
// again. Mirrors the MaxTeams cap test (CWE-770).
func TestCreateSessionEnginesCapped(t *testing.T) {
	const maxEngines = 2
	var closed atomic.Int32
	svc := cappedSelectorService(t, maxEngines, &closed)
	ctx := context.Background()
	sel := server.ProviderSelector{ProviderID: "openrouter"}

	// Fill the registry to the cap.
	ids := make([]session.SessionID, 0, maxEngines)
	for i := 0; i < maxEngines; i++ {
		sess, err := svc.CreateSessionWithProvider(ctx, "/ws", session.ModeDefault, session.Limits{}, sel)
		if err != nil {
			t.Fatalf("CreateSessionWithProvider #%d: %v", i, err)
		}
		ids = append(ids, sess.ID)
	}

	// The (N+1)th is rejected at the Service layer with the cap sentinel.
	_, err := svc.CreateSessionWithProvider(ctx, "/ws", session.ModeDefault, session.Limits{}, sel)
	if !errors.Is(err, server.ErrTooManySessionEngines) {
		t.Fatalf("create past cap: err = %v, want ErrTooManySessionEngines", err)
	}

	// And over gRPC it maps to ResourceExhausted.
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	_, gerr := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{ProviderId: "openrouter"})
	if status.Code(gerr) != codes.ResourceExhausted {
		t.Fatalf("gRPC create past cap: code = %v, want ResourceExhausted (err=%v)", status.Code(gerr), gerr)
	}

	// EndSession frees a slot (and ran the per-session close), so a new create succeeds.
	if eerr := svc.EndSession(ctx, ids[0]); eerr != nil {
		t.Fatalf("EndSession: %v", eerr)
	}
	if closed.Load() < 1 {
		t.Fatalf("EndSession did not run the per-session close (closed=%d)", closed.Load())
	}
	if _, err := svc.CreateSessionWithProvider(ctx, "/ws", session.ModeDefault, session.Limits{}, sel); err != nil {
		t.Fatalf("create after freeing a slot: %v", err)
	}
}
