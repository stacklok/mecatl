package memory

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// fakeStore is an in-memory tool.MemoryStore for tool tests — no filesystem.
type fakeStore struct {
	mu      sync.Mutex
	m       map[string]tool.MemoryEntry
	records map[string]tool.MemoryRecord
}

func newFakeStore() *fakeStore {
	return &fakeStore{m: map[string]tool.MemoryEntry{}, records: map[string]tool.MemoryRecord{}}
}

func (f *fakeStore) Remember(_ context.Context, e tool.MemoryEntry, expected tool.MemoryCurrent) (tool.MemoryRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, exists := f.records[e.Key]
	if exists != expected.Exists || exists && current.Current.Version != expected.Version {
		return tool.MemoryRecord{}, &tool.MemoryVersionConflictError{Key: e.Key, Expected: expected.Version, Actual: current.Current.Version}
	}
	e.UpdatedAt = time.Now()
	version := tool.MemoryVersion("fake-v1")
	if exists {
		version = "fake-v2"
	}
	revision := tool.MemoryRevision{Key: e.Key, Value: e.Value, Description: e.Description, Version: version, Status: tool.MemoryStatusActive, UpdatedAt: e.UpdatedAt}
	current.Current = revision
	current.Revisions = append(current.Revisions, revision)
	f.m[e.Key] = e
	f.records[e.Key] = current
	return current, nil
}

func (f *fakeStore) Inspect(_ context.Context, key string) (tool.MemoryRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.records[key]
	return record, ok, nil
}

func (f *fakeStore) Index(_ context.Context) ([]tool.MemoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]tool.MemoryEntry, 0, len(f.m))
	for k, e := range f.m {
		desc := e.Description
		if desc == "" {
			desc = e.Value
		}
		out = append(out, tool.MemoryEntry{Key: k, Description: desc, UpdatedAt: e.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeStore) Recall(_ context.Context, key string) (tool.MemoryEntry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.m[key]
	return e, ok, nil
}

func (f *fakeStore) List(_ context.Context, prefix string) ([]tool.MemoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tool.MemoryEntry
	for k, e := range f.m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeStore) Search(_ context.Context, query string, k int) ([]tool.MemoryEntry, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	entries := make([]tool.MemoryEntry, 0, len(f.m))
	for key, e := range f.m {
		entries = append(entries, tool.MemoryEntry{
			Key:         key,
			Value:       e.Value,
			Description: descriptionOrFirstLine(e.Description, e.Value),
			UpdatedAt:   e.UpdatedAt,
		})
	}
	// Reuse the real ranker (same package) so the fake mirrors the Store.
	return bm25Rank(entries, query, k), nil
}

func (f *fakeStore) Forget(_ context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.records[key]
	if !ok || record.Current.Version != expected {
		return tool.MemoryRecord{}, &tool.MemoryVersionConflictError{Key: key, Expected: expected, Actual: record.Current.Version}
	}
	delete(f.m, key)
	revision := tool.MemoryRevision{Key: key, Version: "fake-deleted", Status: tool.MemoryStatusDeleted}
	record.Current = revision
	record.Revisions = append(record.Revisions, revision)
	f.records[key] = record
	return record, nil
}

func (*fakeStore) Undo(context.Context, string, tool.MemoryVersion) (tool.MemoryRecord, error) {
	return tool.MemoryRecord{}, errors.New("not implemented")
}

var _ tool.MemoryStore = (*fakeStore)(nil)

func seedFake(t *testing.T, store *fakeStore, entry tool.MemoryEntry) {
	t.Helper()
	if _, err := store.Remember(context.Background(), entry, tool.MemoryCurrent{}); err != nil {
		t.Fatal(err)
	}
}

// call builds a ToolCall with JSON args marshalled from m.
func call(t *testing.T, name string, m map[string]any) session.ToolCall {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return session.NewToolCall(session.ToolCallID("id-"+name), name, raw)
}

// exec runs a tool and fails on a harness-level error.
func exec(t *testing.T, tl tool.Tool, in session.ToolCall) session.ToolResult {
	t.Helper()
	res, err := tl.Execute(context.Background(), in, tool.Environment{})
	if err != nil {
		t.Fatalf("%s: unexpected harness error: %v", tl.Spec().Name, err)
	}
	return res
}

func TestRememberToolWrites(t *testing.T) {
	fs := newFakeStore()
	res := exec(t, NewRememberTool(fs), call(t, "Remember", map[string]any{
		"key": "pref/test-runner", "value": "gotestsum",
	}))
	if res.IsError {
		t.Fatalf("Remember errored: %s", res.Content)
	}
	got, ok, _ := fs.Recall(context.Background(), "pref/test-runner")
	if !ok || got.Value != "gotestsum" {
		t.Errorf("store after Remember: ok=%v value=%q", ok, got.Value)
	}
}

func TestRecallToolReturnsValue(t *testing.T) {
	fs := newFakeStore()
	seedFake(t, fs, tool.MemoryEntry{Key: "pref/editor", Value: "vim"})
	res := exec(t, NewRecallTool(fs), call(t, "Recall", map[string]any{"key": "pref/editor"}))
	if res.IsError {
		t.Fatalf("Recall errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "vim") {
		t.Errorf("Recall content = %q, want it to contain the value", res.Content)
	}
}

func TestRecallToolMissingKeyIsNotError(t *testing.T) {
	fs := newFakeStore()
	res := exec(t, NewRecallTool(fs), call(t, "Recall", map[string]any{"key": "nope"}))
	if res.IsError {
		t.Errorf("Recall of missing key must NOT be an error result: %s", res.Content)
	}
	if !strings.Contains(strings.ToLower(res.Content), "no memory found") {
		t.Errorf("expected a clear 'not found' result, got %q", res.Content)
	}
}

func TestRecallToolPrefixLists(t *testing.T) {
	fs := newFakeStore()
	seedFake(t, fs, tool.MemoryEntry{Key: "pref/a", Value: "1"})
	seedFake(t, fs, tool.MemoryEntry{Key: "pref/b", Value: "2"})
	seedFake(t, fs, tool.MemoryEntry{Key: "other/x", Value: "9"})
	res := exec(t, NewRecallTool(fs), call(t, "Recall", map[string]any{"key": "pref/"}))
	if res.IsError {
		t.Fatalf("Recall prefix errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "pref/a") || !strings.Contains(res.Content, "pref/b") {
		t.Errorf("prefix recall missing entries: %q", res.Content)
	}
	if strings.Contains(res.Content, "other/x") {
		t.Errorf("prefix recall leaked non-matching entry: %q", res.Content)
	}
}

func TestRememberAcceptsDescriptionAndEchoesIndexLine(t *testing.T) {
	fs := newFakeStore()
	res := exec(t, NewRememberTool(fs), call(t, "Remember", map[string]any{
		"key": "pref/test-runner", "value": "gotestsum --format dots", "description": "preferred test runner",
	}))
	if res.IsError {
		t.Fatalf("Remember errored: %s", res.Content)
	}
	// Mutation receipts identify the key but never echo descriptions or values.
	if !strings.Contains(res.Content, "pref/test-runner") || strings.Contains(res.Content, "preferred test runner") || strings.Contains(res.Content, "gotestsum") {
		t.Errorf("Remember receipt should identify the key without value data, got %q", res.Content)
	}
	// The description was persisted.
	got, _, _ := fs.Recall(context.Background(), "pref/test-runner")
	if got.Description != "preferred test runner" {
		t.Errorf("stored description = %q, want it persisted", got.Description)
	}
}

func TestRememberWithoutDescriptionDoesNotEchoValue(t *testing.T) {
	fs := newFakeStore()
	res := exec(t, NewRememberTool(fs), call(t, "Remember", map[string]any{
		"key": "k", "value": "first line\nsecond line",
	}))
	if res.IsError {
		t.Fatalf("Remember errored: %s", res.Content)
	}
	if strings.Contains(res.Content, "first line") || strings.Contains(res.Content, "second line") {
		t.Errorf("Remember receipt leaked value data, got %q", res.Content)
	}
}

func TestRecallStillFetchesFullValue(t *testing.T) {
	fs := newFakeStore()
	seedFake(t, fs, tool.MemoryEntry{
		Key: "pref/editor", Value: "vim, with a long full body", Description: "short",
	})
	res := exec(t, NewRecallTool(fs), call(t, "Recall", map[string]any{"key": "pref/editor"}))
	if res.IsError {
		t.Fatalf("Recall errored: %s", res.Content)
	}
	// Tier-1 load returns the FULL value, not just the index description.
	if !strings.Contains(res.Content, "vim, with a long full body") {
		t.Errorf("Recall should return the full value, got %q", res.Content)
	}
}

func TestMemoryToolsReadOnlyFlags(t *testing.T) {
	fs := newFakeStore()
	if NewRecallTool(fs).ReadOnly() != true {
		t.Error("Recall.ReadOnly() must be true")
	}
	if NewRememberTool(fs).ReadOnly() != false {
		t.Error("Remember.ReadOnly() must be false")
	}
}

func TestMemoryToolsMalformedArgs(t *testing.T) {
	fs := newFakeStore()
	bad := session.NewToolCall("id", "Remember", json.RawMessage("{not json"))
	res := exec(t, NewRememberTool(fs), bad)
	if !res.IsError {
		t.Error("malformed Remember args should be a tool error")
	}
	res = exec(t, NewRecallTool(fs), session.NewToolCall("id", "Recall", json.RawMessage("{not json")))
	if !res.IsError {
		t.Error("malformed Recall args should be a tool error")
	}
	// Missing required args also error.
	if !exec(t, NewRememberTool(fs), call(t, "Remember", map[string]any{"key": "k"})).IsError {
		t.Error("Remember without value should be a tool error")
	}
	if !exec(t, NewRecallTool(fs), call(t, "Recall", map[string]any{})).IsError {
		t.Error("Recall without key should be a tool error")
	}
}

func TestMemoryToolsRegistration(t *testing.T) {
	fs := newFakeStore()
	if len(Tools(fs)) != 6 {
		t.Fatalf("Tools() = %d, want 6", len(Tools(fs)))
	}
	cat := tool.NewCatalog()
	if err := Register(cat, fs); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, name := range []string{"Recall", "Remember", "SearchMemory", "InspectMemory", "ForgetMemory", "UndoMemory"} {
		if _, ok := cat.Lookup(name); !ok {
			t.Errorf("catalog missing %q after Register", name)
		}
	}
}

func TestMemoryToolSpecsHaveDocs(t *testing.T) {
	fs := newFakeStore()
	for _, tl := range Tools(fs) {
		s := tl.Spec()
		if len(s.Description) < 80 {
			t.Errorf("%s: description too short to be onboarding docs", s.Name)
		}
		// The discovery tools steer against the over-eager-memory anti-pattern.
		if (s.Name == RememberToolName || s.Name == RecallToolName || s.Name == SearchMemoryToolName) && !strings.Contains(strings.ToLower(s.Description), "when not to use") {
			t.Errorf("%s: description lacks a 'when NOT to use' section", s.Name)
		}
		var js any
		if err := json.Unmarshal(s.Schema, &js); err != nil {
			t.Errorf("%s: schema invalid JSON: %v", s.Name, err)
		}
	}
}

func TestSearchMemoryRanksRelevantFirst(t *testing.T) {
	fs := newFakeStore()
	seedFake(t, fs, tool.MemoryEntry{
		Key: "pref/test-runner", Value: "Run tests with gotestsum", Description: "preferred test runner",
	})
	seedFake(t, fs, tool.MemoryEntry{
		Key: "project/deploy-gate", Value: "staging deploy needs manual approval", Description: "deploy gate",
	})
	seedFake(t, fs, tool.MemoryEntry{
		Key: "pref/editor", Value: "vim", Description: "favourite editor",
	})

	res := exec(t, NewSearchMemoryTool(fs), call(t, "SearchMemory", map[string]any{
		"query": "preferred test runner",
	}))
	if res.IsError {
		t.Fatalf("SearchMemory errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "pref/test-runner") {
		t.Fatalf("expected the relevant key in results:\n%s", res.Content)
	}
	// The relevant key must rank ahead of the unrelated ones by byte position.
	rel := strings.Index(res.Content, "pref/test-runner")
	for _, other := range []string{"project/deploy-gate", "pref/editor"} {
		if oi := strings.Index(res.Content, other); oi >= 0 && oi < rel {
			t.Errorf("%q ranked before pref/test-runner:\n%s", other, res.Content)
		}
	}
}

func TestSearchMemoryOmitsValues(t *testing.T) {
	fs := newFakeStore()
	const secret = "SECRET-VALUE-SHOULD-NOT-RENDER"
	seedFake(t, fs, tool.MemoryEntry{
		Key: "pref/test-runner", Value: secret, Description: "preferred test runner",
	})
	res := exec(t, NewSearchMemoryTool(fs), call(t, "SearchMemory", map[string]any{
		"query": "preferred test runner",
	}))
	if res.IsError {
		t.Fatalf("SearchMemory errored: %s", res.Content)
	}
	if strings.Contains(res.Content, secret) {
		t.Errorf("SearchMemory leaked a stored value:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "pref/test-runner") {
		t.Errorf("SearchMemory should still return the key:\n%s", res.Content)
	}
}

func TestSearchMemoryEmptyQueryIsError(t *testing.T) {
	fs := newFakeStore()
	res := exec(t, NewSearchMemoryTool(fs), call(t, "SearchMemory", map[string]any{"query": "   "}))
	if !res.IsError {
		t.Errorf("empty/whitespace query should be a tool error, got %q", res.Content)
	}
}

func TestSearchMemoryNoHitIsNotError(t *testing.T) {
	fs := newFakeStore()
	seedFake(t, fs, tool.MemoryEntry{Key: "pref/editor", Value: "vim"})
	res := exec(t, NewSearchMemoryTool(fs), call(t, "SearchMemory", map[string]any{
		"query": "kubernetes deployment topology",
	}))
	if res.IsError {
		t.Errorf("a no-hit search must NOT be an error result: %s", res.Content)
	}
	if !strings.Contains(strings.ToLower(res.Content), "no memory entries match") {
		t.Errorf("expected a clear no-hit message, got %q", res.Content)
	}
}

func TestSearchMemoryReadOnly(t *testing.T) {
	fs := newFakeStore()
	if !NewSearchMemoryTool(fs).ReadOnly() {
		t.Error("SearchMemory.ReadOnly() must be true")
	}
}

func TestNewMemoryToolsNilStorePanics(t *testing.T) {
	for _, ctor := range []func(tool.MemoryStore) tool.Tool{NewRememberTool, NewRecallTool, NewSearchMemoryTool} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("constructor with nil store should panic")
				}
			}()
			_ = ctor(nil)
		}()
	}
}
