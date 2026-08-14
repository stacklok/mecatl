package dream

import (
	"context"
	"errors"
	"iter"
	"sort"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
)

// fakeStore is an in-memory tool.MemoryStore for offline tests. It records how
// many mutating calls it received so a test can assert "no mutation".
type fakeStore struct {
	entries  map[string]tool.MemoryEntry
	remember int
	forget   int
}

func newFakeStore(kv map[string]string) *fakeStore {
	s := &fakeStore{entries: map[string]tool.MemoryEntry{}}
	for k, v := range kv {
		s.entries[k] = tool.MemoryEntry{Key: k, Value: v, UpdatedAt: time.Now().UTC()}
	}
	return s
}

func (s *fakeStore) RememberEntry(_ context.Context, e tool.MemoryEntry) error {
	s.remember++
	e.UpdatedAt = time.Now().UTC()
	s.entries[e.Key] = e
	return nil
}

func (s *fakeStore) Remember(ctx context.Context, key, value string) error {
	return s.RememberEntry(ctx, tool.MemoryEntry{Key: key, Value: value})
}

func (s *fakeStore) Index(_ context.Context) ([]tool.MemoryEntry, error) {
	out := make([]tool.MemoryEntry, 0, len(s.entries))
	for k, e := range s.entries {
		out = append(out, tool.MemoryEntry{Key: k, Description: e.Description, UpdatedAt: e.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *fakeStore) Recall(_ context.Context, key string) (tool.MemoryEntry, bool, error) {
	e, ok := s.entries[key]
	return e, ok, nil
}

func (s *fakeStore) List(_ context.Context, prefix string) ([]tool.MemoryEntry, error) {
	out := make([]tool.MemoryEntry, 0, len(s.entries))
	for k, e := range s.entries {
		if prefix == "" || len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (*fakeStore) Search(_ context.Context, _ string, _ int) ([]tool.MemoryEntry, error) {
	return nil, nil
}

func (s *fakeStore) Forget(_ context.Context, key string) error {
	s.forget++
	delete(s.entries, key)
	return nil
}

func (s *fakeStore) mutations() int { return s.remember + s.forget }

// stubPlanner is a scripted planner returning a fixed Plan/error and recording
// whether it was consulted.
type stubPlanner struct {
	plan   Plan
	err    error
	called bool
}

func (p *stubPlanner) Plan(_ context.Context, _ []tool.MemoryEntry) (Plan, error) {
	p.called = true
	return p.plan, p.err
}

func bigStore(n int) *fakeStore {
	kv := map[string]string{}
	for i := 0; i < n; i++ {
		kv[string(rune('a'+i))] = "v"
	}
	return newFakeStore(kv)
}

// --- below MinEntriesToRun: no-op, no LLM call, no mutation ------------------

func TestBelowMinEntriesIsNoOp(t *testing.T) {
	store := newFakeStore(map[string]string{"a": "1", "b": "2"})
	p := &stubPlanner{plan: Plan{Forgets: []string{"a"}}}
	c := newWithPlanner(store, p, Config{MinEntriesToRun: 5})

	rep, err := c.Consolidate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.called {
		t.Fatal("planner must NOT be consulted below MinEntriesToRun")
	}
	if store.mutations() != 0 {
		t.Fatalf("store must be unchanged, got %d mutations", store.mutations())
	}
	if rep.Kept != 2 || rep.Merged != 0 || rep.Forgotten != 0 {
		t.Fatalf("unexpected report: %+v", rep)
	}
}

// --- merge two near-duplicate entries ---------------------------------------

func TestMergeFoldsDuplicates(t *testing.T) {
	store := newFakeStore(map[string]string{
		"pref/a": "uses pytest",
		"pref/b": "test runner is pytest",
		"pref/c": "keep me",
		"pref/d": "and me",
		"pref/e": "me too",
	})
	p := &stubPlanner{plan: Plan{
		Merges: []Merge{{Into: "pref/a", From: []string{"pref/b"}, Value: "test runner: pytest"}},
	}}
	c := newWithPlanner(store, p, Config{MinEntriesToRun: 3})

	rep, err := c.Consolidate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := store.entries["pref/b"]; ok {
		t.Fatal("merge duplicate pref/b should have been forgotten")
	}
	got, ok := store.entries["pref/a"]
	if !ok {
		t.Fatal("merge target pref/a should survive")
	}
	if got.Value != "test runner: pytest" {
		t.Fatalf("merge target value not tightened, got %q", got.Value)
	}
	if rep.Merged != 1 {
		t.Fatalf("Merged: want 1, got %d", rep.Merged)
	}
	if rep.Forgotten != 0 {
		t.Fatalf("Forgotten: want 0, got %d", rep.Forgotten)
	}
	// 5 input, 1 forgotten → 4 kept.
	if rep.Kept != 4 {
		t.Fatalf("Kept: want 4, got %d", rep.Kept)
	}
}

func TestMergeWithoutValueKeepsExistingValue(t *testing.T) {
	store := newFakeStore(map[string]string{
		"a": "survivor value", "b": "dup", "c": "x", "d": "y", "e": "z",
	})
	p := &stubPlanner{plan: Plan{Merges: []Merge{{Into: "a", From: []string{"b"}}}}}
	c := newWithPlanner(store, p, Config{MinEntriesToRun: 3})

	if _, err := c.Consolidate(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.entries["a"].Value != "survivor value" {
		t.Fatalf("merge without value must keep existing target value, got %q", store.entries["a"].Value)
	}
	if _, ok := store.entries["b"]; ok {
		t.Fatal("duplicate b should be forgotten")
	}
}

// --- fail-safe: planner error or unparseable output leaves store unchanged ---

func TestPlannerErrorLeavesStoreUnchanged(t *testing.T) {
	store := bigStore(6)
	p := &stubPlanner{err: errors.New("model exploded")}
	c := newWithPlanner(store, p, Config{MinEntriesToRun: 3})

	_, err := c.Consolidate(context.Background())
	if err == nil {
		t.Fatal("want error from planner failure")
	}
	if store.mutations() != 0 {
		t.Fatalf("fail-safe violated: store mutated %d times on planner error", store.mutations())
	}
}

func TestUnparseableLLMOutputLeavesStoreUnchanged(t *testing.T) {
	store := bigStore(6)
	// Real llmPlanner path: model returns prose with no JSON object → unparseable.
	llm := mockllm.New(mockllm.TextTurn("I could not decide, sorry."))
	c := New(store, llm, Config{MinEntriesToRun: 3})

	_, err := c.Consolidate(context.Background())
	if err == nil {
		t.Fatal("want error from unparseable output")
	}
	if store.mutations() != 0 {
		t.Fatalf("fail-safe violated: store mutated %d times on unparseable output", store.mutations())
	}
}

func TestEmptyPlanIsNoOpMutation(t *testing.T) {
	store := bigStore(6)
	// Well-formed but empty plan: valid, applies nothing.
	llm := mockllm.New(mockllm.TextTurn(`{"merges":[],"forgets":[]}`))
	c := New(store, llm, Config{MinEntriesToRun: 3})

	rep, err := c.Consolidate(context.Background())
	if err != nil {
		t.Fatalf("empty plan should not error: %v", err)
	}
	if store.mutations() != 0 {
		t.Fatalf("empty plan must not mutate, got %d", store.mutations())
	}
	if rep.Kept != 6 {
		t.Fatalf("Kept: want 6, got %d", rep.Kept)
	}
}

// --- invented-key safety: never introduce a brand-new key -------------------

func TestInventedKeysAreRejected(t *testing.T) {
	store := newFakeStore(map[string]string{
		"a": "1", "b": "2", "c": "3", "d": "4", "e": "5",
	})
	p := &stubPlanner{plan: Plan{
		// Forget a key that does not exist, and a merge into/from invented keys.
		Forgets: []string{"does-not-exist"},
		Merges: []Merge{
			{Into: "invented-target", From: []string{"a"}, Value: "x"}, // bad target
			{Into: "b", From: []string{"also-invented"}, Value: "y"},   // bad source
		},
	}}
	c := newWithPlanner(store, p, Config{MinEntriesToRun: 3})

	rep, err := c.Consolidate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No new key may ever appear.
	for _, bad := range []string{"invented-target", "also-invented", "does-not-exist"} {
		if _, ok := store.entries[bad]; ok {
			t.Fatalf("invented key %q must never be written", bad)
		}
	}
	// All original keys remain (nothing valid was actioned).
	if len(store.entries) != 5 {
		t.Fatalf("store should still hold 5 entries, got %d", len(store.entries))
	}
	if rep.Merged != 0 || rep.Forgotten != 0 {
		t.Fatalf("nothing should have been actioned: %+v", rep)
	}
}

func TestMergeValueNeverWritesNewKey(t *testing.T) {
	store := newFakeStore(map[string]string{
		"a": "1", "b": "2", "c": "3", "d": "4", "e": "5",
	})
	before := make(map[string]struct{})
	for k := range store.entries {
		before[k] = struct{}{}
	}
	p := &stubPlanner{plan: Plan{Merges: []Merge{{Into: "a", From: []string{"b", "c"}, Value: "merged"}}}}
	c := newWithPlanner(store, p, Config{MinEntriesToRun: 3})

	if _, err := c.Consolidate(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Every surviving key must have existed in the input set.
	for k := range store.entries {
		if _, ok := before[k]; !ok {
			t.Fatalf("new key %q appeared; only existing keys may survive", k)
		}
	}
}

// --- forget cap respected ---------------------------------------------------

func TestForgetCapRespected(t *testing.T) {
	store := newFakeStore(map[string]string{
		"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6",
	})
	p := &stubPlanner{plan: Plan{Forgets: []string{"a", "b", "c", "d", "e"}}}
	c := newWithPlanner(store, p, Config{MinEntriesToRun: 3, MaxForgets: 2})

	rep, err := c.Consolidate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.forget != 2 {
		t.Fatalf("forget cap violated: want 2 forgets, got %d", store.forget)
	}
	if rep.Forgotten != 2 {
		t.Fatalf("Forgotten: want 2 (capped), got %d", rep.Forgotten)
	}
	if len(store.entries) != 4 {
		t.Fatalf("want 4 entries remaining after capped forgets, got %d", len(store.entries))
	}
}

func TestForgetCapAppliesAcrossMergesAndForgets(t *testing.T) {
	store := newFakeStore(map[string]string{
		"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6",
	})
	// Merge folds b,c into a (2 deletions); then forgets d,e (would be 2 more) but
	// the cap of 3 leaves room for exactly one more.
	p := &stubPlanner{plan: Plan{
		Merges:  []Merge{{Into: "a", From: []string{"b", "c"}}},
		Forgets: []string{"d", "e"},
	}}
	c := newWithPlanner(store, p, Config{MinEntriesToRun: 3, MaxForgets: 3})

	rep, err := c.Consolidate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.forget != 3 {
		t.Fatalf("global cap violated: want 3 total forgets, got %d", store.forget)
	}
	if rep.Merged != 2 {
		t.Fatalf("Merged: want 2, got %d", rep.Merged)
	}
	if rep.Forgotten != 1 {
		t.Fatalf("Forgotten: want 1 (cap left room for one), got %d", rep.Forgotten)
	}
}

func TestNegativeMaxForgetsDisablesDeletion(t *testing.T) {
	store := bigStore(6)
	p := &stubPlanner{plan: Plan{Forgets: []string{"a", "b"}}}
	c := newWithPlanner(store, p, Config{MinEntriesToRun: 3, MaxForgets: -1})

	rep, err := c.Consolidate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.forget != 0 {
		t.Fatalf("negative MaxForgets must disable deletion, got %d forgets", store.forget)
	}
	if rep.Forgotten != 0 {
		t.Fatalf("Forgotten: want 0, got %d", rep.Forgotten)
	}
}

// --- ctx cancellation honoured ----------------------------------------------

func TestContextCancelBeforeRun(t *testing.T) {
	store := bigStore(6)
	p := &stubPlanner{plan: Plan{Forgets: []string{"a"}}}
	c := newWithPlanner(store, p, Config{MinEntriesToRun: 3})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Consolidate(ctx)
	if err == nil {
		t.Fatal("want error on cancelled ctx")
	}
	if p.called {
		t.Fatal("planner must not be consulted with a cancelled ctx")
	}
	if store.mutations() != 0 {
		t.Fatalf("cancelled ctx must not mutate store, got %d", store.mutations())
	}
}

func TestContextCancelDuringPlanFailsSafe(t *testing.T) {
	store := bigStore(6)
	// blockingProvider blocks until ctx is done, exercising the per-run timeout /
	// cancellation path inside the real llmPlanner.
	c := New(store, blockingProvider{}, Config{MinEntriesToRun: 3, Timeout: 20 * time.Millisecond})

	_, err := c.Consolidate(context.Background())
	if err == nil {
		t.Fatal("want error when the plan call times out")
	}
	if store.mutations() != 0 {
		t.Fatalf("timeout must leave store unchanged, got %d", store.mutations())
	}
}

// --- end-to-end through the real llmPlanner + mockllm -----------------------

func TestConsolidateViaScriptedLLM(t *testing.T) {
	store := newFakeStore(map[string]string{
		"pref/a": "uses pytest", "pref/b": "pytest is the runner",
		"c": "x", "d": "y", "e": "z",
	})
	llm := mockllm.New(mockllm.TextTurn(
		`Here is the plan: {"merges":[{"into":"pref/a","from":["pref/b"],"value":"runner: pytest"}],"forgets":["c"]}`,
	))
	c := New(store, llm, Config{MinEntriesToRun: 3})

	rep, err := c.Consolidate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := store.entries["pref/b"]; ok {
		t.Fatal("pref/b should be merged away")
	}
	if _, ok := store.entries["c"]; ok {
		t.Fatal("c should be forgotten")
	}
	if store.entries["pref/a"].Value != "runner: pytest" {
		t.Fatalf("pref/a not tightened, got %q", store.entries["pref/a"].Value)
	}
	if rep.Merged != 1 || rep.Forgotten != 1 {
		t.Fatalf("report: want Merged=1 Forgotten=1, got %+v", rep)
	}
	if llm.Calls() != 1 {
		t.Fatalf("want exactly 1 model call, got %d", llm.Calls())
	}
}

// --- RunPeriodically honours ctx --------------------------------------------

func TestRunPeriodicallyRejectsNonPositiveInterval(t *testing.T) {
	c := New(bigStore(6), mockllm.New(), Config{})
	if err := c.RunPeriodically(context.Background(), 0, nil); err == nil {
		t.Fatal("want error for non-positive interval")
	}
}

func TestRunPeriodicallyStopsOnCtxCancel(t *testing.T) {
	store := bigStore(6)
	p := &stubPlanner{plan: Plan{}}
	c := newWithPlanner(store, p, Config{MinEntriesToRun: 3})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := c.RunPeriodically(ctx, 5*time.Millisecond, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunPeriodically should return ctx error, got %v", err)
	}
}

// blockingProvider blocks until ctx is done, then yields nothing.
type blockingProvider struct{}

func (blockingProvider) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }

func (blockingProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(_ func(port.Chunk, error) bool) {
		<-ctx.Done()
	}, nil
}

// TestPrefixScopesConsolidationToUserNamespace proves Config.Prefix:"user/" limits
// consolidation to the user-model namespace: only "user/"-prefixed entries are
// listed and eligible for mutation; project-memory keys are never touched. This is
// the same Prefix mechanism the user-model consolidator uses (issue #14, Phase 2b).
func TestPrefixScopesConsolidationToUserNamespace(t *testing.T) {
	store := newFakeStore(map[string]string{
		"user/a":    "operator likes terse answers",
		"user/b":    "operator likes terse replies",
		"user/c":    "operator background: Go",
		"pref/x":    "project test runner",
		"project/y": "deploy gate",
	})
	// A merge that folds user/b into user/a, plus a forget of pref/x — but pref/x is
	// OUTSIDE the prefix, so it is never even listed and the forget is a no-invented-
	// key no-op (the project key must survive).
	p := &stubPlanner{plan: Plan{
		Merges:  []Merge{{Into: "user/a", From: []string{"user/b"}, Value: "operator likes terse answers"}},
		Forgets: []string{"pref/x"},
	}}
	c := newWithPlanner(store, p, Config{MinEntriesToRun: 3, Prefix: "user/"})

	rep, err := c.Consolidate(context.Background())
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if rep.Merged != 1 {
		t.Errorf("expected 1 merged user/ entry, got %+v", rep)
	}
	// user/b folded away; user/a, user/c remain.
	if _, ok := store.entries["user/b"]; ok {
		t.Errorf("user/b should have been folded into user/a")
	}
	if _, ok := store.entries["user/a"]; !ok {
		t.Errorf("user/a (merge survivor) should remain")
	}
	// The project-namespace keys are untouched: they were outside the prefix, so the
	// pref/x forget is a no-op (no-invented-keys still holds — pref/x was not in the
	// listed input).
	if _, ok := store.entries["pref/x"]; !ok {
		t.Errorf("pref/x is outside the user/ prefix and must NOT be forgotten")
	}
	if _, ok := store.entries["project/y"]; !ok {
		t.Errorf("project/y is outside the user/ prefix and must survive")
	}
}

type dreamRacingMemory struct {
	*memmemory.Store
	beforeCAS func(context.Context)
}

func (m *dreamRacingMemory) RememberIfCurrent(ctx context.Context, entry tool.MemoryEntry, expected tool.MemoryCurrent) (tool.MemoryRecord, error) {
	if m.beforeCAS != nil {
		fn := m.beforeCAS
		m.beforeCAS = nil
		fn(ctx)
	}
	return m.Store.RememberIfCurrent(ctx, entry, expected)
}

func TestLifecycleDreamDoesNotOverwriteConcurrentPromotion(t *testing.T) {
	ctx := context.Background()
	store := &dreamRacingMemory{Store: memmemory.New()}
	for _, entry := range []tool.MemoryEntry{
		{Key: "project/a", Value: "old a"},
		{Key: "project/b", Value: "old b"},
		{Key: "project/c", Value: "old c"},
	} {
		if err := store.RememberEntry(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	store.beforeCAS = func(ctx context.Context) {
		promotion := tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{Writer: tool.MemoryWriterModel, Origin: tool.MemoryOriginLearning, Source: tool.MemorySource{ProposalID: "proposal-race"}})
		if err := store.RememberEntry(promotion, tool.MemoryEntry{Key: "project/a", Value: "promoted value"}); err != nil {
			t.Error(err)
		}
	}
	consolidator := newWithPlanner(store, &stubPlanner{plan: Plan{Merges: []Merge{{Into: "project/a", From: []string{"project/b"}, Value: "dream value"}}}}, Config{MinEntriesToRun: 3})
	if _, err := consolidator.Consolidate(ctx); err == nil {
		t.Fatal("concurrent promotion did not trip dream CAS")
	}
	current, found, err := store.Inspect(ctx, "project/a")
	if err != nil || !found {
		t.Fatalf("inspect: found=%v err=%v", found, err)
	}
	if current.Current.Value != "promoted value" || current.Current.Source.ProposalID != "proposal-race" {
		t.Fatalf("concurrent promotion overwritten: %#v", current.Current)
	}
	if _, found, err = store.Recall(ctx, "project/b"); err != nil || !found {
		t.Fatalf("merge source removed after failed CAS: found=%v err=%v", found, err)
	}
}
