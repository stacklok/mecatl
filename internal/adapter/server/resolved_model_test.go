package server_test

import (
	"context"
	"testing"
	"time"

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

// newResolvedModelService builds a Service whose shared/default engine resolves to
// DefaultResolvedModel, with the supplied per-session factory. It mirrors
// newMCPServiceStore but pins a DefaultResolvedModel so the default-path echo can be
// asserted.
func newResolvedModelService(t *testing.T, dflt server.ResolvedModel, factory server.SessionEngineFactory) *server.Service {
	t.Helper()
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("SHARED")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:               shared,
		Store:                memstore.New(),
		Workspaces:           func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		DefaultLimits:        session.Limits{MaxTurns: 10, MaxToolCalls: 20},
		Now:                  func() time.Time { return time.Unix(0, 0) },
		SessionEngine:        factory,
		DefaultResolvedModel: dflt,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// newResolvedModelServiceWithResolver mirrors newResolvedModelService but also wires
// the live-first Config.ResolveContextWindow closure (issue #66).
func newResolvedModelServiceWithResolver(t *testing.T, dflt server.ResolvedModel, factory server.SessionEngineFactory, resolve func(p, m string) int64) *server.Service {
	t.Helper()
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("SHARED")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:               shared,
		Store:                memstore.New(),
		Workspaces:           func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		DefaultLimits:        session.Limits{MaxTurns: 10, MaxToolCalls: 20},
		Now:                  func() time.Time { return time.Unix(0, 0) },
		SessionEngine:        factory,
		DefaultResolvedModel: dflt,
		ResolveContextWindow: resolve,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestServiceResolvedModelLiveFirstWindow (issue #66): a DEFAULT session whose model
// is in the live listing but NOT the curated catalog bakes ContextWindow == 0
// (no footer bar). With ResolveContextWindow wired, the default-path echo overlays
// the live window at call time — provider/model identity stays the baked value.
func TestServiceResolvedModelLiveFirstWindow(t *testing.T) {
	// The baked seed: a live-only model, catalog floor 0 (the bug repro).
	dflt := server.ResolvedModel{ProviderID: "openrouter", ModelID: "openai/gpt-5.5", ContextWindow: 0}
	resolve := func(p, m string) int64 {
		if p == "openrouter" && m == "openai/gpt-5.5" {
			return 1_050_000
		}
		return 0
	}
	svc := newResolvedModelServiceWithResolver(t, dflt, nil, resolve)

	sess, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider(zero): %v", err)
	}
	got := svc.ResolvedModel(sess.ID)
	if got.ContextWindow != 1_050_000 {
		t.Fatalf("ResolvedModel.ContextWindow = %d, want the live-first 1050000 (issue #66 overlay)", got.ContextWindow)
	}
	if got.ProviderID != "openrouter" || got.ModelID != "openai/gpt-5.5" {
		t.Fatalf("ResolvedModel identity = %s/%s, want the verbatim baked openrouter/openai/gpt-5.5", got.ProviderID, got.ModelID)
	}
}

// TestServiceResolvedModelLiveOnlyDefaultProvisionalThenHeals (issue #66 provisional-0):
// a LIVE-ONLY DEFAULT model (in the live listing but not the curated catalog) bakes a
// PROVISIONAL 0 window at create time (the echo resolver returns 0 while the live
// refresh is in flight), so the default-path footer-heal gate fires. The resolver is
// consulted on EVERY ResolvedModel read (resolve-at-use), so once the refresh settles
// and reports the live window the next GetSession heals — no rebuild. This mirrors the
// SELECTOR-path heal but on the default branch, the path the baked-seed-via-echo-
// resolver fix opened.
func TestServiceResolvedModelLiveOnlyDefaultProvisionalThenHeals(t *testing.T) {
	// The baked seed is a PROVISIONAL 0 (the echo resolver's pre-completion value for a
	// live-only default model).
	dflt := server.ResolvedModel{ProviderID: "openrouter", ModelID: "openai/gpt-5.5", ContextWindow: 0}
	const liveWindow = 1_050_000
	// A mutable live window standing in for the echo resolver: 0 pre-completion
	// (provisional), liveWindow once the refresh settles + the live entry lands.
	live := int64(0)
	resolve := func(p, m string) int64 {
		if p == "openrouter" && m == "openai/gpt-5.5" {
			return live
		}
		return 0
	}
	svc := newResolvedModelServiceWithResolver(t, dflt, nil, resolve)

	sess, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider(zero): %v", err)
	}
	// Pre-completion: the default echoes the provisional 0 (the client refetches).
	if got := svc.ResolvedModel(sess.ID); got.ContextWindow != 0 {
		t.Fatalf("pre-completion default echo ContextWindow = %d, want 0 (provisional, live-only default)", got.ContextWindow)
	}
	// The refresh settles and reports the live window.
	live = liveWindow
	got := svc.ResolvedModel(sess.ID)
	if got.ContextWindow != liveWindow {
		t.Fatalf("post-completion default echo ContextWindow = %d, want the healed live %d (resolve-at-use, NO rebuild)", got.ContextWindow, liveWindow)
	}
	if got.ProviderID != "openrouter" || got.ModelID != "openai/gpt-5.5" {
		t.Fatalf("default identity = %s/%s, want the verbatim openrouter/openai/gpt-5.5", got.ProviderID, got.ModelID)
	}
}

// TestServiceResolvedModelNilResolverByteIdentical: with ResolveContextWindow nil
// (the memstore/driver/test paths), the default-path echo is the verbatim baked
// DefaultResolvedModel — byte-identical to the pre-issue-#66 behaviour.
func TestServiceResolvedModelNilResolverByteIdentical(t *testing.T) {
	dflt := server.ResolvedModel{ProviderID: "openai", ModelID: "gpt-default", ContextWindow: 128000}
	svc := newResolvedModelServiceWithResolver(t, dflt, nil, nil) // nil resolver

	sess, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider(zero): %v", err)
	}
	if got := svc.ResolvedModel(sess.ID); got != dflt {
		t.Fatalf("nil-resolver ResolvedModel = %+v, want the verbatim baked %+v (byte-identical to pre-#66)", got, dflt)
	}
}

// TestServiceResolvedModelResolverZeroKeepsBaked (issue #66 review gap): a DEFAULT
// session with a baked NON-ZERO DefaultResolvedModel.ContextWindow and a
// ResolveContextWindow returning 0 (not-yet-swapped / unknown to the live store) must
// echo the BAKED window — the resolver's 0 must NOT clobber a known baked value. This
// pins the `if w > 0` overlay guard.
//
// MUTATION-VERIFY: dropping the `if w > 0` guard in Service.ResolvedModel (so the
// resolver's 0 overwrites rm.ContextWindow) makes this fail.
func TestServiceResolvedModelResolverZeroKeepsBaked(t *testing.T) {
	const baked = 200000
	dflt := server.ResolvedModel{ProviderID: "openai", ModelID: "gpt-baked", ContextWindow: baked}
	// A resolver that returns 0 for everything (pre-swap: the live store has no entry
	// for this model and — unlike contextWindowFor — this stand-in does not floor to
	// the catalog, so it returns a bare 0).
	resolve := func(_, _ string) int64 { return 0 }
	svc := newResolvedModelServiceWithResolver(t, dflt, nil, resolve)

	sess, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider(zero): %v", err)
	}
	if got := svc.ResolvedModel(sess.ID); got.ContextWindow != baked {
		t.Fatalf("ResolvedModel.ContextWindow = %d, want the baked %d (a resolver 0 must NOT clobber a known baked window)", got.ContextWindow, baked)
	}
}

// TestServiceResolvedModelResolverNeverLowers (issue #66 regression): a CATALOGUED
// default model with the resolver wired still echoes its catalog window — the
// resolver returns the catalog floor when no live entry exists, so the fix never
// lowers a known window. (A zero from the resolver keeps the baked seed.)
func TestServiceResolvedModelResolverNeverLowers(t *testing.T) {
	dflt := server.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5", ContextWindow: 400000}
	// Resolver returns the catalog floor for the catalogued model (mirrors
	// contextWindowFor falling back to the catalog on a live miss).
	resolve := func(p, m string) int64 {
		if p == "openai" && m == "gpt-5" {
			return 400000
		}
		return 0
	}
	svc := newResolvedModelServiceWithResolver(t, dflt, nil, resolve)

	sess, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider(zero): %v", err)
	}
	if got := svc.ResolvedModel(sess.ID); got != dflt {
		t.Fatalf("ResolvedModel = %+v, want the unchanged catalogued %+v (fix must never lower a known window)", got, dflt)
	}
}

// TestServiceResolvedModelSelectorLiveFirst is the SELECTOR-PATH resolve-at-use guard
// (the exact path the band-aids missed): a per-session SELECTOR session's echo window
// is resolved LIVE-FIRST at call time via Config.ResolveContextWindow over the SAME
// (provider, model) identity the factory registered — NOT a create-time frozen scalar.
// A selector that resolves to 0 at create time (live-only model, pre-swap) and to the
// live window after the catalog swap HEALS on the next ResolvedModel read, with no
// engine rebuild — proving the frozen per-session ContextWindow field is gone.
func TestServiceResolvedModelSelectorLiveFirst(t *testing.T) {
	dflt := server.ResolvedModel{ProviderID: "openai", ModelID: "gpt-default", ContextWindow: 128000}
	const liveWindow = 1_050_000
	perSession := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("PER-SESSION")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "openai/gpt-5.5",
	})
	factory := func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		return server.SessionEngineResult{
			Engine:     perSession,
			ProviderID: sel.ProviderID,
			ModelID:    sel.ModelID,
			Close:      func() error { return nil },
		}, nil
	}
	// A mutable live window: 0 pre-swap (live-only model, catalog floor), liveWindow
	// after the swap. The resolver is consulted on EVERY ResolvedModel call (resolve-
	// at-use), so the echo heals without any rebuild.
	live := int64(0)
	resolve := func(p, m string) int64 {
		if p == "openrouter" && m == "openai/gpt-5.5" {
			return live
		}
		return 0
	}
	svc := newResolvedModelServiceWithResolver(t, dflt, factory, resolve)

	sel := server.ProviderSelector{ProviderID: "openrouter", ModelID: "openai/gpt-5.5"}
	sess, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{}, sel)
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	// Pre-swap: the live-only selector resolves to 0 (the frozen-scalar bug would echo
	// 0 forever).
	if got := svc.ResolvedModel(sess.ID); got.ContextWindow != 0 {
		t.Fatalf("pre-swap selector echo ContextWindow = %d, want 0 (live-only, pre-swap)", got.ContextWindow)
	}
	// THE SWAP: the live catalog now reports the window.
	live = liveWindow
	got := svc.ResolvedModel(sess.ID)
	if got.ContextWindow != liveWindow {
		t.Fatalf("post-swap selector echo ContextWindow = %d, want the live %d (resolve-at-use — NO rebuild)", got.ContextWindow, liveWindow)
	}
	if got.ProviderID != "openrouter" || got.ModelID != "openai/gpt-5.5" {
		t.Fatalf("selector identity = %s/%s, want the verbatim openrouter/openai/gpt-5.5", got.ProviderID, got.ModelID)
	}
}

// TestServiceResolvedModelDefaultPath: a zero-selector session (no per-session
// engine) reports Config.DefaultResolvedModel verbatim — the same composition
// single-source discipline as SessionCapabilities. The value is NOT read back from
// the request (which carries an empty model_id for the default session).
func TestServiceResolvedModelDefaultPath(t *testing.T) {
	dflt := server.ResolvedModel{ProviderID: "openai", ModelID: "gpt-default", ContextWindow: 128000}
	svc := newResolvedModelService(t, dflt, nil)

	sess, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider(zero): %v", err)
	}
	got := svc.ResolvedModel(sess.ID)
	if got != dflt {
		t.Fatalf("ResolvedModel(default session) = %+v, want %+v (the composition DefaultResolvedModel)", got, dflt)
	}
	// An unregistered id also falls back to the default (mirrors SessionCapabilities).
	if got := svc.ResolvedModel("no-such-session"); got != dflt {
		t.Fatalf("ResolvedModel(unknown) = %+v, want the default %+v", got, dflt)
	}
}

// TestServiceResolvedModelPerSession: an explicit selector registers a per-session
// engine carrying the factory's resolved ProviderID/ModelID IDENTITY; the context
// WINDOW is resolved LIVE-FIRST at echo time via the SAME Config.ResolveContextWindow
// the engine reads (the resolve-at-use unification), so ResolvedModel returns the
// resolved identity + the live window (not the default). After CloseSession evicts the
// per-session engine, it falls back to the default.
func TestServiceResolvedModelPerSession(t *testing.T) {
	dflt := server.ResolvedModel{ProviderID: "openai", ModelID: "gpt-default", ContextWindow: 128000}
	perSession := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("PER-SESSION")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "anthropic/claude-opus-4.5",
	})
	factory := func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		return server.SessionEngineResult{
			Engine:     perSession,
			ProviderID: sel.ProviderID,
			ModelID:    sel.ModelID,
			Close:      func() error { return nil },
		}, nil
	}
	// The resolver supplies the live-first window for the selected model — the SAME
	// source the engine reads, so the per-session echo carries it.
	resolve := func(p, m string) int64 {
		if p == "anthropic" && m == "claude-opus-4.5" {
			return 200000
		}
		return 0
	}
	svc := newResolvedModelServiceWithResolver(t, dflt, factory, resolve)

	sel := server.ProviderSelector{ProviderID: "anthropic", ModelID: "claude-opus-4.5"}
	sess, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{}, sel)
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	want := server.ResolvedModel{ProviderID: "anthropic", ModelID: "claude-opus-4.5", ContextWindow: 200000}
	if got := svc.ResolvedModel(sess.ID); got != want {
		t.Fatalf("ResolvedModel(per-session) = %+v, want the resolved selector %+v", got, want)
	}
	svc.CloseSession(sess.ID)
	if got := svc.ResolvedModel(sess.ID); got != dflt {
		t.Fatalf("ResolvedModel after CloseSession = %+v, want fallback to default %+v", got, dflt)
	}
}

// TestGRPCCreateSessionEchoesResolvedModel: the gRPC CreateSession handler echoes
// resolved_model from Service.ResolvedModel for both the default path AND an
// explicit selector — asserting the EFFECTIVE (resolved) values cross the wire, not
// the raw request fields.
func TestGRPCCreateSessionEchoesResolvedModel(t *testing.T) {
	dflt := server.ResolvedModel{ProviderID: "openai", ModelID: "gpt-default", ContextWindow: 128000}
	factory := func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		return server.SessionEngineResult{
			Engine:     agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("X")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "m"}),
			ProviderID: sel.ProviderID,
			ModelID:    sel.ModelID,
			Close:      func() error { return nil },
		}, nil
	}
	// The resolver supplies BOTH the default window (gpt-default → 128000) and the
	// selector window (anthropic/claude-opus-4.5 → 200000), live-first for either branch.
	resolve := func(p, m string) int64 {
		switch {
		case p == "openai" && m == "gpt-default":
			return 128000
		case p == "anthropic" && m == "claude-opus-4.5":
			return 200000
		}
		return 0
	}
	svc := newResolvedModelServiceWithResolver(t, dflt, factory, resolve)
	h := server.NewHarnessServer(svc)

	t.Run("default selector echoes the composition default", func(t *testing.T) {
		resp, err := h.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		rm := resp.GetResolvedModel()
		if rm.GetProviderId() != "openai" || rm.GetModelId() != "gpt-default" || rm.GetContextWindow() != 128000 {
			t.Fatalf("resolved_model = %+v, want the composition default openai/gpt-default/128000", rm)
		}
	})

	t.Run("explicit selector echoes the resolved values, not the raw request model", func(t *testing.T) {
		// The request model_id is "claude-opus-4.5"; the factory resolved it under the
		// "anthropic" provider with a 200000 window. The echo must reflect the RESOLVED
		// values from Service.ResolvedModel, not be a naive read-back of the request.
		resp, err := h.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
			ProviderId: "anthropic",
			ModelId:    "claude-opus-4.5",
		})
		if err != nil {
			t.Fatalf("CreateSession(selector): %v", err)
		}
		rm := resp.GetResolvedModel()
		if rm.GetProviderId() != "anthropic" || rm.GetModelId() != "claude-opus-4.5" || rm.GetContextWindow() != 200000 {
			t.Fatalf("resolved_model = %+v, want anthropic/claude-opus-4.5/200000", rm)
		}
	})
}

// TestGRPCGetSessionEchoesResolvedModel proves the gRPC GetSession handler threads
// Service.ResolvedModel(sess.ID) into the Session snapshot (toProtoSession), not a
// zero ResolvedModel. Uses an explicit selector so the per-session resolved value is
// distinct from the default — a handler dropping it would echo zero and fail.
func TestGRPCGetSessionEchoesResolvedModel(t *testing.T) {
	dflt := server.ResolvedModel{ProviderID: "openai", ModelID: "gpt-default", ContextWindow: 128000}
	factory := func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		return server.SessionEngineResult{
			Engine:     agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("X")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "m"}),
			ProviderID: sel.ProviderID,
			ModelID:    sel.ModelID,
			Close:      func() error { return nil },
		}, nil
	}
	resolve := func(p, m string) int64 {
		if p == "anthropic" && m == "claude-opus-4.5" {
			return 200000
		}
		return 0
	}
	svc := newResolvedModelServiceWithResolver(t, dflt, factory, resolve)
	h := server.NewHarnessServer(svc)

	createResp, err := h.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
		ProviderId: "anthropic",
		ModelId:    "claude-opus-4.5",
	})
	if err != nil {
		t.Fatalf("CreateSession(selector): %v", err)
	}
	getResp, err := h.GetSession(context.Background(), &mecatlv1.GetSessionRequest{SessionId: createResp.GetSessionId()})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	rm := getResp.GetSession().GetResolvedModel()
	if rm.GetProviderId() != "anthropic" || rm.GetModelId() != "claude-opus-4.5" || rm.GetContextWindow() != 200000 {
		t.Fatalf("Session snapshot resolved_model = %+v, want the Service-resolved anthropic/claude-opus-4.5/200000 (handler must thread Service.ResolvedModel, not zero)", rm)
	}
}
