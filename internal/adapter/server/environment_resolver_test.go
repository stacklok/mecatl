package server_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/remoteenv"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/tools"
)

type resolverPlacementProvider struct {
	resolve func(context.Context, session.EnvironmentRef) (tool.Environment, error)
}

func (resolverPlacementProvider) Bind(_ context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if req.Selector.Kind == server.PlacementSelectorNoFS {
		ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
		return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, nofs.New(), nil)}, nil
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), nil)}, nil
}

func (p resolverPlacementProvider) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if req.Ref.Kind == session.EnvKindLocal {
		return server.PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, memfs.NewWorkspace(req.Ref.ID), nil)}, nil
	}
	if p.resolve == nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	env, err := p.resolve(ctx, req.Ref)
	if err != nil {
		return server.PlacementBinding{}, err
	}
	return server.PlacementBinding{Ref: req.Ref, Environment: env}, nil
}

// newEnvTestService builds a minimal Service for the environment-resolver tests.
// It returns the service, the backing store (so a test can persist a session
// carrying a non-in-tree ref directly), and a factory-call counter so a test
// can assert the resolver path does NOT trigger per-session engine rehydration.
func newEnvTestService(t *testing.T, resolver func(context.Context, session.EnvironmentRef) (tool.Environment, error)) (*server.Service, port.SessionStore, *int32) {
	t.Helper()
	return newEnvTestServiceWithLLM(t, resolver, mockllm.New(mockllm.TextTurn("ok")), nil)
}

// newEnvTestServiceWithLLM is the tools-bearing variant: it registers the given
// tools on the shared engine's catalog and scripts the LLM. Used by tests that
// must prove a tool actually executes against the RESOLVED Environment's
// Workspace/runner (not just that StartRun returns).
func newEnvTestServiceWithLLM(t *testing.T, resolver func(context.Context, session.EnvironmentRef) (tool.Environment, error), llm *mockllm.Provider, tl []tool.Tool) (*server.Service, port.SessionStore, *int32) {
	t.Helper()
	var factoryCalls int32
	cat := tool.NewCatalog()
	for _, tli := range tl {
		cat.MustRegister(tli)
	}
	shared := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
	})
	store := memstore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine: shared,
		Store:  store,

		DefaultLimits:     session.Limits{MaxTurns: 5},
		Now:               func() time.Time { return time.Unix(0, 0) },
		PlacementProvider: resolverPlacementProvider{resolve: resolver},
		PlacementScope:    "test",
		SessionEngine: func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
			factoryCalls++
			return server.SessionEngineResult{Engine: shared}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, store, &factoryCalls
}

// remoteSessionWithRef persists a session carrying a non-in-tree EnvironmentRef
// directly through the store, so a subsequent StartRun exercises the resolver
// path (createSession stamps only local/nofs refs).
func remoteSessionWithRef(t *testing.T, store port.SessionStore, ref session.EnvironmentRef) *session.Session {
	t.Helper()
	sess := session.New("remote-1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "remote-ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 5}, time.Unix(0, 0))
	sess.EnvironmentRef = ref
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
	return sess
}

// TestEnvironmentResolverMissingFailsLoudly proves a persisted non-in-tree ref
// with NO EnvironmentResolver wired fails loudly with ErrFailedPrecondition —
// never a silent local fallback.
func TestEnvironmentResolverMissingFailsLoudly(t *testing.T) {
	svc, store, factoryCalls := newEnvTestService(t, nil)
	ref := session.EnvironmentRef{Kind: remoteenv.Kind, ID: "ns-1", Revision: "r1"}
	sess := remoteSessionWithRef(t, store, ref)

	_, err := svc.StartRun(context.Background(), sess.ID, "go")
	if !errors.Is(err, server.ErrPlacementUnavailable) {
		t.Fatalf("StartRun = %v, want ErrPlacementUnavailable (no provider reattachment)", err)
	}
	if *factoryCalls != 0 {
		t.Fatalf("factory called %d times, want 0 (resolver is independent of engine rehydration)", *factoryCalls)
	}
}

// TestEnvironmentResolverWrongRefFailsLoudly proves a resolver that returns an
// Environment whose Ref() does NOT equal the requested ref fails loudly.
func TestEnvironmentResolverWrongRefFailsLoudly(t *testing.T) {
	b := remoteenv.NewBackend()
	other, _ := b.NewEnvironment("other")
	svc, store, _ := newEnvTestService(t, func(_ context.Context, _ session.EnvironmentRef) (tool.Environment, error) {
		// Return a DIFFERENT ref than requested.
		return other, nil
	})
	ref := session.EnvironmentRef{Kind: remoteenv.Kind, ID: "ns-1", Revision: "r1"}
	sess := remoteSessionWithRef(t, store, ref)

	_, err := svc.StartRun(context.Background(), sess.ID, "go")
	if !errors.Is(err, server.ErrInvalidPlacementBinding) {
		t.Fatalf("StartRun = %v, want ErrInvalidPlacementBinding (ref mismatch)", err)
	}
	if err == nil || !strings.Contains(err.Error(), server.ErrInvalidPlacementBinding.Error()) {
		t.Fatalf("StartRun error = %v, want invalid exact binding", err)
	}
}

// TestEnvironmentResolverNilWorkspaceFailsLoudly proves a resolver that returns
// an Environment with a nil Workspace fails loudly.
func TestEnvironmentResolverNilWorkspaceFailsLoudly(t *testing.T) {
	svc, store, _ := newEnvTestService(t, func(_ context.Context, _ session.EnvironmentRef) (tool.Environment, error) {
		// A zero Environment has a nil Workspace.
		return tool.Environment{}, nil
	})
	ref := session.EnvironmentRef{Kind: remoteenv.Kind, ID: "ns-1", Revision: "r1"}
	sess := remoteSessionWithRef(t, store, ref)

	_, err := svc.StartRun(context.Background(), sess.ID, "go")
	if !errors.Is(err, server.ErrInvalidPlacementBinding) {
		t.Fatalf("StartRun = %v, want ErrInvalidPlacementBinding (nil workspace)", err)
	}
	if err == nil || !strings.Contains(err.Error(), server.ErrInvalidPlacementBinding.Error()) {
		t.Fatalf("StartRun error = %v, want invalid exact binding", err)
	}
}

// TestEnvironmentResolverReattachesAndRuns proves a correctly-wired resolver
// reattaches a live Environment for a persisted ref and the run executes
// against it — driving a Read tool call THROUGH the resolved Environment's
// Workspace so `remote-seed` reaches the model and the final tool-result text,
// without triggering engine rehydration for a default provider/model.
func TestEnvironmentResolverReattachesAndRuns(t *testing.T) {
	ctx := context.Background()
	b := remoteenv.NewBackend()
	env, err := b.NewEnvironment("main")
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	ref := env.Ref()
	// Seed a file the resolver-reattached workspace will expose.
	if _, err := env.Workspace().CreateFile(ctx, "seed.txt", []byte("remote-seed")); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}

	// Script: turn 1 calls Read on seed.txt; turn 2 reports the content back.
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"seed.txt"}`)),
		mockllm.TextTurn("done"),
	)
	svc, store, factoryCalls := newEnvTestServiceWithLLM(t, b.Resolve, llm, []tool.Tool{&tools.ReadTool{}})
	sess := remoteSessionWithRef(t, store, ref)

	run, err := svc.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// Collect the tool-result content + final text. drainServerRun auto-allows
	// any ask (the AllowAllFloorRules floor pre-approves Read).
	var toolResultText, final string
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			toolResultText = ev.ToolResult.Content
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			final = ev.Result.Text
		}
	}
	if final != "done" {
		t.Fatalf("run reply = %q, want %q", final, "done")
	}
	if !strings.Contains(toolResultText, "remote-seed") {
		t.Fatalf("Read tool result = %q, want one containing %q (the Read must execute against the RESOLVED Environment's workspace)", toolResultText, "remote-seed")
	}
	if *factoryCalls != 1 {
		t.Fatalf("factory called %d times, want 1 (remote root differs from the shared engine policy root)", *factoryCalls)
	}

	// The session's persisted ref survives the run (the run stamped the default
	// only on a ZERO ref; a non-zero ref is preserved verbatim).
	loaded, lerr := svc.GetSession(ctx, sess.ID)
	if lerr != nil {
		t.Fatalf("GetSession: %v", lerr)
	}
	if loaded.EnvironmentRef != ref {
		t.Fatalf("persisted EnvironmentRef = %+v, want %+v (a non-zero ref must be preserved, not overwritten)", loaded.EnvironmentRef, ref)
	}
}

// TestEnvironmentResolverReattachesAndRunsReadAndBash (issue #462 phase-3 finding
// #2) proves a correctly-wired resolver reattaches a live Environment and the
// run executes BOTH a Read tool call and a Bash tool call THROUGH the resolved
// Environment's Workspace AND CommandRunner, so `remote-seed` reaches BOTH tool
// results — not just the file-API Read, but the fake Bash runner's `cat` against
// the SAME namespace. This closes the gap left by the Read-only
// TestEnvironmentResolverReattachesAndRuns: a remote Environment whose runner is
// nil (or pointing at the wrong namespace) would pass the Read test but fail
// here. The floor (AllowAllFloorRules) pre-approves the read-only `cat`, so no
// permission ask is surfaced.
func TestEnvironmentResolverReattachesAndRunsReadAndBash(t *testing.T) {
	ctx := context.Background()
	b := remoteenv.NewBackend()
	env, err := b.NewEnvironment("main")
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	ref := env.Ref()
	if _, err := env.Workspace().CreateFile(ctx, "seed.txt", []byte("remote-seed")); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}

	// Script: turn 1 calls Read; turn 2 calls Bash `cat seed.txt`; turn 3 reports
	// the content back.
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"seed.txt"}`)),
		mockllm.ToolCallTurn(call("c2", "Bash", `{"command":"cat seed.txt"}`)),
		mockllm.TextTurn("done"),
	)
	svc, store, factoryCalls := newEnvTestServiceWithLLM(t, b.Resolve, llm, []tool.Tool{&tools.ReadTool{}, tools.NewBashTool()})
	sess := remoteSessionWithRef(t, store, ref)

	run, err := svc.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// Collect the two tool results + final text. The AllowAllFloorRules floor
	// pre-approves the read-only `cat`, so no ask is surfaced.
	var readResult, bashResult, final string
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			switch string(ev.ToolResult.CallID) {
			case "c1":
				readResult = ev.ToolResult.Content
			case "c2":
				bashResult = ev.ToolResult.Content
			}
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			final = ev.Result.Text
		}
	}
	if final != "done" {
		t.Fatalf("run reply = %q, want %q", final, "done")
	}
	if !strings.Contains(readResult, "remote-seed") {
		t.Fatalf("Read tool result = %q, want one containing %q (Read must execute against the RESOLVED Environment's workspace)", readResult, "remote-seed")
	}
	if !strings.Contains(bashResult, "remote-seed") {
		t.Fatalf("Bash tool result = %q, want one containing %q (the resolved Environment's CommandRunner must observe the SAME namespace — cat seed.txt must reach the resolved workspace)", bashResult, "remote-seed")
	}
	if *factoryCalls != 1 {
		t.Fatalf("factory called %d times, want 1 (remote root differs from the shared engine policy root)", *factoryCalls)
	}
}

// TestEnvironmentResolverDoesNotRebuildSessionEngine proves a default-FS session
// (no selector, default profile) whose workspace resolves through the DEFAULT
// path — NOT the resolver — does NOT trigger per-session engine rehydration
// even when an EnvironmentResolver is wired (the resolver is inert for the
// in-tree Kinds).
func TestEnvironmentResolverDoesNotRebuildSessionEngine(t *testing.T) {
	called := false
	svc, _, factoryCalls := newEnvTestService(t, func(_ context.Context, _ session.EnvironmentRef) (tool.Environment, error) {
		called = true
		return tool.Environment{}, nil
	})
	// A plain default-FS session: createSession stamps a local ref, so the
	// resolver must NOT be consulted.
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.EnvironmentRef.Kind != session.EnvKindLocal {
		t.Fatalf("create stamped Kind = %q, want local", sess.EnvironmentRef.Kind)
	}
	if sess.EnvironmentRef.ID != "/ws" {
		t.Fatalf("create stamped ID = %q, want opaque test placement", sess.EnvironmentRef.ID)
	}

	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainServerRun(run); got != "ok" {
		t.Fatalf("run reply = %q, want ok", got)
	}
	if called {
		t.Fatal("EnvironmentResolver was called for a default-FS session; it must be inert for in-tree Kinds")
	}
	if *factoryCalls != 0 {
		t.Fatalf("factory called %d times, want 0 (default session must not rehydrate)", *factoryCalls)
	}

	// Finding #6: the create-stamped ref and the LIVE Environment's ref (stamped
	// at run entry from defaultEnvironmentRef, the SINGLE shared derivation) must
	// agree. The persisted ref after a run is the live ref stamped on the next
	// save, so it must equal the create-stamped ref — proving the two derivations
	// did not drift (one function, not two).
	loaded, lerr := svc.GetSession(context.Background(), sess.ID)
	if lerr != nil {
		t.Fatalf("GetSession: %v", lerr)
	}
	wantRef := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	if loaded.EnvironmentRef != wantRef {
		t.Fatalf("post-run EnvironmentRef = %+v, want %+v (the live ref stamped at run entry must equal the create-stamped ref — one shared derivation)", loaded.EnvironmentRef, wantRef)
	}
}

// newEnvTestServiceWithFactory builds a Service whose SessionEngine factory
// RETURNS a real per-session engine (instead of erroring) so a test can exercise
// the legitimate selector-rehydration path for a remote-ref session that ALSO
// carries a non-default provider/model selector. The factory-call counter lets
// the test assert rehydration DID happen while the resolver is STILL consulted.
func newEnvTestServiceWithFactory(t *testing.T, resolver func(context.Context, session.EnvironmentRef) (tool.Environment, error), llm *mockllm.Provider, tl []tool.Tool) (*server.Service, port.SessionStore, *int32) {
	t.Helper()
	var factoryCalls int32
	cat := tool.NewCatalog()
	for _, tli := range tl {
		cat.MustRegister(tli)
	}
	shared := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
	})
	store := memstore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine: shared,
		Store:  store,

		DefaultLimits:     session.Limits{MaxTurns: 5},
		Now:               func() time.Time { return time.Unix(0, 0) },
		PlacementProvider: resolverPlacementProvider{resolve: resolver},
		PlacementScope:    "test",
		SessionEngine: func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, profile server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
			factoryCalls++
			// Mirror a real composition factory: build a per-session engine on the
			// selector's provider/model. For the remote-ref rehydration test the
			// profile MUST be ProfileDefault (NOT no-fs inferred from the empty
			// workspace), proving the profileForSession remote-ref guard holds
			// (issue #462 phase-3 finding #1).
			if profile != server.ProfileDefault {
				return server.SessionEngineResult{}, fmt.Errorf("test factory: profile = %q, want ProfileDefault (the empty-workspace no-fs inference must NOT fire for a remote ref)", profile)
			}
			eng := agent.NewEngine(agent.Deps{
				LLM:     llm,
				Catalog: cat,
				Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
				Model:   sel.ModelID,
			})
			return server.SessionEngineResult{Engine: eng, ProviderID: sel.ProviderID, ModelID: sel.ModelID, BuiltForMode: mode}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, store, &factoryCalls
}

// remoteSessionWithRefAndSelector persists a session carrying a non-in-tree
// EnvironmentRef AND a non-default provider/model selector directly through the
// store, so a subsequent StartRun exercises the selector-rehydration path. The
// persisted Workspace is empty (a remote session's filesystem lives in the
// remote backend, not on a local root).
func remoteSessionWithRefAndSelector(t *testing.T, store port.SessionStore, ref session.EnvironmentRef, providerID, modelID string) *session.Session {
	t.Helper()
	sess := session.New("remote-sel-1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 5}, time.Unix(0, 0))
	sess.EnvironmentRef = ref
	sess.ProviderID = providerID
	sess.ModelID = modelID
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
	return sess
}

// TestRemoteRefWithSelectorRehydratesEngineAndResolvesEnv (issue #462 phase-3
// finding #1) proves a remote-ref session with an EMPTY persisted Workspace and
// a NON-EMPTY ProviderID rehydrates its per-session engine through the factory
// (the selector arm is independent of the environment) AND STILL reattaches the
// remote Environment through the resolver — the resolver is NOT preempted by a
// no-fs inference from the empty workspace. The run drives a Read tool through
// the RESOLVED Environment's workspace so `remote-seed` reaches the tool result.
func TestRemoteRefWithSelectorRehydratesEngineAndResolvesEnv(t *testing.T) {
	ctx := context.Background()
	b := remoteenv.NewBackend()
	env, err := b.NewEnvironment("main")
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	ref := env.Ref()
	if _, err := env.Workspace().CreateFile(ctx, "seed.txt", []byte("remote-seed")); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}

	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"seed.txt"}`)),
		mockllm.TextTurn("done"),
	)
	svc, store, factoryCalls := newEnvTestServiceWithFactory(t, b.Resolve, llm, []tool.Tool{&tools.ReadTool{}})
	sess := remoteSessionWithRefAndSelector(t, store, ref, "openrouter", "anthropic/claude-3.5-sonnet")

	run, err := svc.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var toolResultText, final string
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			toolResultText = ev.ToolResult.Content
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			final = ev.Result.Text
		}
	}
	if final != "done" {
		t.Fatalf("run reply = %q, want %q", final, "done")
	}
	if !strings.Contains(toolResultText, "remote-seed") {
		t.Fatalf("Read tool result = %q, want one containing %q (the Read must execute against the RESOLVED Environment, not a no-fs override)", toolResultText, "remote-seed")
	}
	if *factoryCalls != 1 {
		t.Fatalf("factory called %d times, want 1 (the selector arm MUST rehydrate the engine)", *factoryCalls)
	}

	loaded, lerr := svc.GetSession(ctx, sess.ID)
	if lerr != nil {
		t.Fatalf("GetSession: %v", lerr)
	}
	if loaded.EnvironmentRef != ref {
		t.Fatalf("persisted EnvironmentRef = %+v, want %+v (the remote ref must survive, not be relabeled no-fs)", loaded.EnvironmentRef, ref)
	}
	if loaded.ProviderID != "openrouter" || loaded.ModelID != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("persisted selector = %q/%q, want openrouter/anthropic/claude-3.5-sonnet", loaded.ProviderID, loaded.ModelID)
	}
}

// TestRemoteRefDefaultProviderEmptyWorkspaceResolvesEnv (issue #462 phase-3
// finding #1) proves a remote-ref session with an EMPTY persisted Workspace and
// NO provider/model selector (the default-provider case) does NOT rehydrate the
// engine (the empty-workspace arm is guarded against the remote ref) and
// reattaches the remote Environment through the resolver. This is the
// regression for the headline bug: the empty-workspace inference must NOT
// relabel a remote session no-fs.
func TestRemoteRefDefaultProviderEmptyWorkspaceResolvesEnv(t *testing.T) {
	ctx := context.Background()
	b := remoteenv.NewBackend()
	env, err := b.NewEnvironment("main")
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	ref := env.Ref()
	if _, err := env.Workspace().CreateFile(ctx, "seed.txt", []byte("remote-seed")); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}

	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"seed.txt"}`)),
		mockllm.TextTurn("done"),
	)
	// The factory ERRORS if called — the empty-workspace arm must NOT fire for a
	// remote ref, so rehydration must not trigger.
	svc, store, factoryCalls := newEnvTestServiceWithLLM(t, b.Resolve, llm, []tool.Tool{&tools.ReadTool{}})
	// Persist a remote-ref session with empty Workspace and no selector.
	sess := session.New("remote-default-1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 5}, time.Unix(0, 0))
	sess.EnvironmentRef = ref
	if err := store.Save(ctx, sess); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	run, err := svc.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var toolResultText, final string
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			toolResultText = ev.ToolResult.Content
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			final = ev.Result.Text
		}
	}
	if final != "done" {
		t.Fatalf("run reply = %q, want %q", final, "done")
	}
	if !strings.Contains(toolResultText, "remote-seed") {
		t.Fatalf("Read tool result = %q, want one containing %q (the resolver must reattach the remote Environment)", toolResultText, "remote-seed")
	}
	if *factoryCalls != 1 {
		t.Fatalf("factory called %d times, want 1 (custom placement root must re-pin policy collaborators)", *factoryCalls)
	}

	loaded, lerr := svc.GetSession(ctx, sess.ID)
	if lerr != nil {
		t.Fatalf("GetSession: %v", lerr)
	}
	if loaded.EnvironmentRef != ref {
		t.Fatalf("persisted EnvironmentRef = %+v, want %+v", loaded.EnvironmentRef, ref)
	}
}

// TestNoFSCreateSessionStampsNoFSRef (issue #462 phase-3 finding #3) proves a
// no-fs CreateSession stamps {Kind:EnvKindNoFS, ID:""} as the EnvironmentRef,
// the persisted snapshot round-trips it verbatim, and a reloaded session keeps
// it. This is the default-ref derivation contract for the in-tree no-fs backend:
// the stamped ref must always match the live Environment's ref.
func TestNoFSCreateSessionStampsNoFSRef(t *testing.T) {
	ctx := context.Background()
	perSession := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("ok")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	store := memstore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("shared")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		}),
		Store: store,

		DefaultLimits: session.Limits{MaxTurns: 5},
		Now:           func() time.Time { return time.Unix(0, 0) },
		SessionEngine: func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{Engine: perSession, Close: func() error { return nil }}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	wantRef := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
	sess, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileNoFS)
	if err != nil {
		t.Fatalf("CreateSessionWithProfile: %v", err)
	}
	if sess.EnvironmentRef != wantRef {
		t.Fatalf("create-stamped EnvironmentRef = %+v, want %+v", sess.EnvironmentRef, wantRef)
	}

	// The persisted snapshot round-trips the ref verbatim.
	loaded, lerr := svc.GetSession(ctx, sess.ID)
	if lerr != nil {
		t.Fatalf("GetSession: %v", lerr)
	}
	if loaded.EnvironmentRef != wantRef {
		t.Fatalf("persisted EnvironmentRef = %+v, want %+v", loaded.EnvironmentRef, wantRef)
	}

	// Run a turn; the rehydrated (in-process) engine + override carry the same ref.
	run, rerr := svc.StartRun(ctx, sess.ID, "go")
	if rerr != nil {
		t.Fatalf("StartRun: %v", rerr)
	}
	if got := drainServerRun(run); got != "ok" {
		t.Fatalf("run reply = %q, want ok", got)
	}

	// After the run the persisted ref is STILL the nofs ref (not overwritten).
	loaded2, lerr2 := svc.GetSession(ctx, sess.ID)
	if lerr2 != nil {
		t.Fatalf("GetSession after run: %v", lerr2)
	}
	if loaded2.EnvironmentRef != wantRef {
		t.Fatalf("post-run EnvironmentRef = %+v, want %+v", loaded2.EnvironmentRef, wantRef)
	}
}
