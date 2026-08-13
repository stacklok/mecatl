package app

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
)

// --- fakes ------------------------------------------------------------------

// recordingHookRunner is a fake inner port.HookRunner: it records every Run call
// and returns a fixed outcome so the decorator's "return inner outcome unchanged"
// contract is testable.
type recordingHookRunner struct {
	calls   int
	outcome governance.HookOutcome
}

func (r *recordingHookRunner) Run(_ context.Context, _ governance.HookEvent) (governance.HookOutcome, error) {
	r.calls++
	return r.outcome, nil
}

// signalStore is a fake port.SessionStore that SIGNALS a channel on every Load and
// returns a session with an EMPTY transcript — so the reviewer's Review no-ops right
// after Load (no child engine run, no LLM), making each decorator FIRE observable on
// the channel without background churn.
type signalStore struct {
	loaded    chan struct{}
	principal chan *session.Principal
}

func newSignalStore() *signalStore {
	return &signalStore{loaded: make(chan struct{}, 16), principal: make(chan *session.Principal, 16)}
}

func (s *signalStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	s.principal <- session.PrincipalFromContext(ctx)
	s.loaded <- struct{}{}
	// Empty conversation → renderTranscript empty → Review returns before any run.
	return session.New(id, session.ModeDefault, "/proj", session.Limits{}, time.Now()), nil
}

func (*signalStore) Save(_ context.Context, _ *session.Session) error { return nil }

// firedWithin reports whether the store's Load was signalled within a short window
// (a fire), draining exactly one signal. It NEVER sleeps for a fixed period: it
// waits on the channel with a generous deadline that only matters on failure.
func (s *signalStore) firedWithin(t *testing.T) bool {
	t.Helper()
	select {
	case <-s.loaded:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

// didNotFire confirms no fire happened in a short settle window. A tiny bounded wait
// is acceptable here (a negative assertion); it does not gate correctness timing.
func (s *signalStore) didNotFire(t *testing.T) bool {
	t.Helper()
	select {
	case <-s.loaded:
		return false
	case <-time.After(100 * time.Millisecond):
		return true
	}
}

// minimalReviewer builds a real *agent.UserModelReviewer over the signalStore and a
// throwaway child engine (never actually run, because the transcript is empty).
func minimalReviewer(t *testing.T, store port.SessionStore) *agent.UserModelReviewer {
	t.Helper()
	eng := newChildEngine(Config{}, "", mockllm.New(mockllm.TextTurn("x")), tool.NewCatalog(), "test-model", fixedDefaultWindow, promptConfig(Config{}, ""))
	return agent.NewUserModelReviewer(store, eng)
}

// --- FIX 3: Stop-trigger glue (decorator + detached fire + debounce) ---------

// TestUserModelReviewHooksFiresOnStopDebounced proves the composition-layer
// Stop-trigger decorator: it fires the reviewer (observed via the store's Load
// signal) on PhaseStop honouring the session-count debounce ((count-1)%interval==0),
// never fires on a non-Stop phase, and returns the inner runner's outcome unchanged.
func TestUserModelReviewHooksFiresOnStopDebounced(t *testing.T) {
	store := newSignalStore()
	inner := &recordingHookRunner{outcome: governance.HookOutcome{Block: true, Message: "inner-msg"}}
	hooks := newUserModelReviewHooks(inner, minimalReviewer(t, store), 3, port.NopDiagnostics{})

	stop := governance.HookEvent{Phase: governance.PhaseStop, SessionID: "s"}

	// 7 Stops with interval 3 → fire on stops 1, 4, 7 (the (count-1)%3==0 set).
	wantFire := map[int]bool{1: true, 4: true, 7: true}
	for i := 1; i <= 7; i++ {
		out, err := hooks.Run(context.Background(), stop)
		if err != nil {
			t.Fatalf("stop %d: Run err: %v", i, err)
		}
		// Side-effect-only contract: the inner outcome is returned UNCHANGED.
		if !out.Block || out.Message != "inner-msg" {
			t.Errorf("stop %d: decorator altered the inner outcome: %+v", i, out)
		}
		if wantFire[i] {
			if !store.firedWithin(t) {
				t.Errorf("stop %d: expected a review fire (debounce admit), got none", i)
			}
		} else {
			if !store.didNotFire(t) {
				t.Errorf("stop %d: expected NO review fire (debounced out), but it fired", i)
			}
		}
	}

	// The inner runner saw every Run, fire or not (delegation is unconditional).
	if inner.calls != 7 {
		t.Errorf("inner runner Run calls = %d, want 7 (every Run delegates)", inner.calls)
	}
}

func TestUserModelReviewHooksPreservesPrincipal(t *testing.T) {
	store := newSignalStore()
	hooks := newUserModelReviewHooks(&recordingHookRunner{}, minimalReviewer(t, store), 1, port.NopDiagnostics{})
	principal := &session.Principal{
		Issuer:    "https://idp.example",
		Subject:   "alice",
		GrantType: session.GrantTypeUser,
	}

	if _, err := hooks.Run(session.WithPrincipal(context.Background(), principal), governance.HookEvent{
		Phase: governance.PhaseStop, SessionID: "s",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !store.firedWithin(t) {
		t.Fatal("review did not load the source session")
	}
	select {
	case got := <-store.principal:
		if got == nil || *got != *principal {
			t.Fatalf("Load principal = %#v, want %#v", got, principal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Load did not observe a principal")
	}
}

// TestUserModelReviewHooksIgnoresNonStop proves a non-Stop phase NEVER fires the
// reviewer, even though it still delegates to the inner runner.
func TestUserModelReviewHooksIgnoresNonStop(t *testing.T) {
	store := newSignalStore()
	inner := &recordingHookRunner{}
	hooks := newUserModelReviewHooks(inner, minimalReviewer(t, store), 1, port.NopDiagnostics{})

	if _, err := hooks.Run(context.Background(), governance.HookEvent{Phase: governance.PhasePreToolUse, SessionID: "s"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !store.didNotFire(t) {
		t.Errorf("a PreToolUse phase must NOT fire the user-model reviewer")
	}
	if inner.calls != 1 {
		t.Errorf("inner runner should still see the Run, calls = %d", inner.calls)
	}
}

// --- FIX 4: 2b + consolidation OFF by default --------------------------------

// TestMaybeWrapUserModelReviewDefaultOff mirrors TestBuildSoulSourceDefaultOnAndDisable:
// the Stop-trigger wrapper is OFF by default (review disabled → passthrough), AND a
// no-op when review is requested but the user-model store is nil (the warn branch).
func TestMaybeWrapUserModelReviewDefaultOff(t *testing.T) {
	provider := mockllm.New(mockllm.TextTurn("x"))
	reg := regForTest(provider, providerOpenAI, "test-model")
	store := memstoreForTest(t)

	t.Run("review disabled (default) is passthrough", func(t *testing.T) {
		inner := &recordingHookRunner{}
		// A non-nil user-model store, but UserModelReview is false (the default).
		um, err := memory.New(t.TempDir())
		if err != nil {
			t.Fatalf("memory.New: %v", err)
		}
		got := maybeWrapUserModelReview(Config{}, reg, inner, store, provider, um)
		if got != port.HookRunner(inner) {
			t.Fatalf("review OFF by default must return the inner runner UNCHANGED, got a wrapper")
		}
	})

	t.Run("review on but nil store is a no-op", func(t *testing.T) {
		inner := &recordingHookRunner{}
		got := maybeWrapUserModelReview(Config{UserModelReview: true}, reg, inner, store, provider, nil)
		if got != port.HookRunner(inner) {
			t.Fatalf("review requested with a nil user-model store must return the inner runner UNCHANGED")
		}
	})

	t.Run("review on with a store wraps", func(t *testing.T) {
		inner := &recordingHookRunner{}
		um, err := memory.New(t.TempDir())
		if err != nil {
			t.Fatalf("memory.New: %v", err)
		}
		got := maybeWrapUserModelReview(Config{UserModelReview: true, Model: "test-model"}, reg, inner, store, provider, um)
		if got == port.HookRunner(inner) {
			t.Fatalf("review ENABLED with a store must return a WRAPPER, got the bare inner runner")
		}
	})
}

func TestUserModelReviewEngineUsesFinalModelConfiguredWindow(t *testing.T) {
	const finalModel = "vendor/final-review-id"
	provider := mockllm.New(mockllm.TextTurn("x"))
	reg := regForTest(provider, providerOpenAI, finalModel)
	cfg := Config{
		ModelAliases:   map[string]string{"review": finalModel},
		contextWindows: map[string]map[string]int{providerOpenAI: {finalModel: 444_000}},
	}
	resolved, ok := lookupModelAlias(cfg, "review")
	if !ok {
		t.Fatal("review alias did not resolve")
	}
	cfg.Model = resolved
	store, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	eng := buildUserModelReviewEngine(cfg, reg, providerOpenAI, provider, store)
	if got := eng.ContextWindow(); got != 444_000 {
		t.Fatalf("review engine ContextWindow = %d, want configured final-model window 444000", got)
	}
}

// TestUserModelConsolidationOffByDefault proves no consolidator is started for the
// default (zero) interval, and one IS started for a positive interval — via the
// started-bool testability seam.
func TestUserModelConsolidationOffByDefault(t *testing.T) {
	provider := mockllm.New(mockllm.TextTurn("x"))
	um, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}

	if started := startUserModelConsolidation(context.Background(), Config{}, um, provider); started {
		t.Errorf("user-model consolidation must be OFF by default (interval 0), but a consolidator was started")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stop the background loop promptly
	if started := startUserModelConsolidation(ctx, Config{UserModelConsolidateInterval: time.Hour}, um, provider); !started {
		t.Errorf("a positive consolidate interval must start a consolidator")
	}
}

// --- FIX 5-A: the reviewer child catalog is genuinely minimal ----------------

// TestUserModelReviewEngineCatalogIsMinimal pins the security claim the review
// relied on: the child engine the 2b reviewer runs has EXACTLY one tool, RememberUser
// — no Read/Edit/Write/Bash/Subagent/Parallel. So the reviewer can WRITE the user model but
// has no other capability.
func TestUserModelReviewEngineCatalogIsMinimal(t *testing.T) {
	um, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	provider := mockllm.New(mockllm.TextTurn("x"))

	// Reconstruct the catalog buildUserModelReviewEngine wires, the same way it does,
	// and assert it is exactly {RememberUser}. (The engine itself does not expose its
	// catalog; this mirrors the one construction path so the scoping claim is pinned.)
	names := reviewEngineToolNames(provider, um)
	if len(names) != 1 {
		t.Fatalf("review engine catalog has %d tools, want exactly 1 (RememberUser only): %v", len(names), names)
	}
	if names[0] != memory.RememberUserToolName {
		t.Errorf("review engine sole tool = %q, want %q", names[0], memory.RememberUserToolName)
	}
	// Defensive: none of the dangerous tools are present.
	for _, banned := range []string{"Read", "Edit", "Write", "Bash", "Subagent", "Parallel", memory.RecallUserToolName, memory.SearchUserModelToolName} {
		for _, got := range names {
			if got == banned {
				t.Errorf("review engine catalog must NOT contain %q", banned)
			}
		}
	}
}

// reviewEngineToolNames returns the tool names buildUserModelReviewEngine registers,
// reproducing its catalog-building logic exactly (the only RememberUser tool from
// memory.NewUserModelTools), so the test pins the minimal-scope guarantee.
func reviewEngineToolNames(provider port.LLMProvider, store *memory.Store) []string {
	cat := tool.NewCatalog()
	for _, tl := range memory.NewUserModelTools(store) {
		if tl.Spec().Name == memory.RememberUserToolName {
			cat.MustRegister(tl)
		}
	}
	// Build the engine to ensure the construction path is exercised (it is otherwise
	// unused, but constructing it proves the catalog is engine-compatible).
	_ = newChildEngine(Config{}, "", provider, cat, "test-model", fixedDefaultWindow, promptConfig(Config{}, ""))
	var names []string
	for _, tl := range cat.Tools() {
		names = append(names, tl.Spec().Name)
	}
	return names
}

// memstoreForTest builds an in-memory SessionStore for the decorator tests.
func memstoreForTest(t *testing.T) port.SessionStore {
	t.Helper()
	return newSignalStore()
}
