package dream

import (
	"context"
	"errors"
	"iter"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
)

type fakeStore struct {
	entries  map[string]tool.MemoryEntry
	remember int
	forget   int
}

func newFakeStore(entries ...tool.MemoryEntry) *fakeStore {
	s := &fakeStore{entries: make(map[string]tool.MemoryEntry, len(entries))}
	for _, entry := range entries {
		s.entries[entry.Key] = entry
	}
	return s
}

func (s *fakeStore) RememberEntry(_ context.Context, entry tool.MemoryEntry) error {
	s.remember++
	s.entries[entry.Key] = entry
	return nil
}
func (s *fakeStore) Remember(ctx context.Context, key, value string) error {
	return s.RememberEntry(ctx, tool.MemoryEntry{Key: key, Value: value})
}
func (s *fakeStore) Index(ctx context.Context) ([]tool.MemoryEntry, error) { return s.List(ctx, "") }
func (s *fakeStore) Recall(_ context.Context, key string) (tool.MemoryEntry, bool, error) {
	entry, ok := s.entries[key]
	return entry, ok, nil
}
func (s *fakeStore) List(_ context.Context, prefix string) ([]tool.MemoryEntry, error) {
	entries := make([]tool.MemoryEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		if strings.HasPrefix(entry.Key, prefix) {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries, nil
}
func (*fakeStore) Search(context.Context, string, int) ([]tool.MemoryEntry, error) { return nil, nil }
func (s *fakeStore) Forget(_ context.Context, key string) error {
	s.forget++
	delete(s.entries, key)
	return nil
}
func (s *fakeStore) mutations() int { return s.remember + s.forget }

type atomicTestStore struct {
	*memmemory.Store
}

func (s *atomicTestStore) RetireDuplicate(ctx context.Context, survivorKey string, survivorVersion tool.MemoryVersion, sourceKey string, sourceVersion tool.MemoryVersion) (tool.MemoryRecord, error) {
	survivor, found, err := s.Inspect(ctx, survivorKey)
	if err != nil {
		return tool.MemoryRecord{}, err
	}
	if !found || survivor.Current.Status != tool.MemoryStatusActive || survivor.Current.Version != survivorVersion {
		var actual tool.MemoryVersion
		if found {
			actual = survivor.Current.Version
		}
		return tool.MemoryRecord{}, &tool.MemoryVersionConflictError{Key: survivorKey, Expected: survivorVersion, Actual: actual}
	}
	return s.ForgetVersioned(ctx, sourceKey, sourceVersion)
}

type recordingPlanner struct {
	mu      sync.Mutex
	batches [][]tool.MemoryEntry
	ops     []supersession
	err     error
}

func (p *recordingPlanner) Plan(_ context.Context, entries []tool.MemoryEntry) ([]supersession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.batches = append(p.batches, append([]tool.MemoryEntry(nil), entries...))
	return p.ops, p.err
}

func entries(keys ...string) []tool.MemoryEntry {
	out := make([]tool.MemoryEntry, 0, len(keys))
	for _, key := range keys {
		out = append(out, tool.MemoryEntry{Key: key, Value: "value-" + key, Description: "description-" + key})
	}
	return out
}

func keys(batch []tool.MemoryEntry) string {
	var b strings.Builder
	for _, entry := range batch {
		b.WriteString(entry.Key)
	}
	return b.String()
}

func TestGeneratePlanDoesNotMutate(t *testing.T) {
	store := newFakeStore(entries("a", "b", "c")...)
	planner := &recordingPlanner{ops: []supersession{{Survivor: "a", Superseded: []string{"b"}}}}
	consolidator := newWithPlanner(store, planner, Config{MinEntriesToRun: 1})

	plan, err := consolidator.GeneratePlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.mutations() != 0 {
		t.Fatalf("plan generation mutated store %d times", store.mutations())
	}
	if len(plan.operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(plan.operations))
	}

	report, err := consolidator.ApplyPlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if report.Planned != 1 || report.Skipped != 1 || report.Applied+report.Conflicted+report.Skipped+report.Failed != report.Planned {
		t.Fatalf("unexpected report: %+v", report)
	}
	if store.mutations() != 0 {
		t.Fatalf("application skeleton mutated base store %d times", store.mutations())
	}
}

func TestParsePlanStrictWholeObject(t *testing.T) {
	valid := `{"supersessions":[{"survivor":"a","superseded":["b"]}]}`
	if _, err := parsePlan(valid); err != nil {
		t.Fatalf("valid plan: %v", err)
	}
	for name, output := range map[string]string{
		"prose prefix":       "plan: " + valid,
		"code fence":         "```json\n" + valid + "\n```",
		"trailing value":     valid + ` {}`,
		"unknown top field":  `{"supersessions":[],"secret":"leak-me"}`,
		"unknown op field":   `{"supersessions":[{"survivor":"a","superseded":["b"],"value":"x"}]}`,
		"duplicate top":      `{"supersessions":[],"supersessions":[]}`,
		"duplicate nested":   `{"supersessions":[{"survivor":"a","survivor":"b","superseded":[]}]}`,
		"escaped duplicate":  `{"supersessions":[{"survivor":"a","\u0073urvivor":"b","superseded":[]}]}`,
		"mixed-case top":     `{"Supersessions":[]}`,
		"mixed-case nested":  `{"supersessions":[{"Survivor":"a","superseded":[]}]}`,
		"case alias":         `{"supersessions":[{"survivor":"a","Survivor":"b","superseded":[]}]}`,
		"top-level array":    `[]`,
		"missing field":      `{}`,
		"missing survivor":   `{"supersessions":[{"superseded":["b"]}]}`,
		"missing superseded": `{"supersessions":[{"survivor":"a"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePlan(output); err == nil {
				t.Fatalf("accepted %q", output)
			}
		})
	}
}

func TestPlannerErrorDoesNotLeakRawOutput(t *testing.T) {
	const secret = "raw-private-memory"
	provider := &captureProvider{reply: `{"supersessions":[],"` + secret + `":true}`}
	consolidator := New(newFakeStore(entries("a")...), provider, Config{MinEntriesToRun: 1})
	_, err := consolidator.GeneratePlan(context.Background())
	if err == nil {
		t.Fatal("expected invalid output error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), provider.reply) {
		t.Fatalf("error leaked model output: %v", err)
	}
}

func TestStrictPlannerFailureNeverMutates(t *testing.T) {
	for _, run := range []struct {
		name string
		call func(*Consolidator) error
	}{
		{name: "generate", call: func(c *Consolidator) error { _, err := c.GeneratePlan(context.Background()); return err }},
		{name: "consolidate", call: func(c *Consolidator) error { _, err := c.Consolidate(context.Background()); return err }},
	} {
		t.Run(run.name, func(t *testing.T) {
			store := newFakeStore(entries("a", "b")...)
			provider := &captureProvider{reply: `{"Supersessions":[]}`}
			if err := run.call(New(store, provider, Config{MinEntriesToRun: 1})); err == nil {
				t.Fatal("expected strict parse failure")
			}
			if store.mutations() != 0 {
				t.Fatalf("strict parse failure caused %d mutations", store.mutations())
			}
		})
	}
}

func TestPlannerOutputLimitFailsWithoutMutation(t *testing.T) {
	store := newFakeStore(entries("a", "b")...)
	provider := &captureProvider{chunks: []string{strings.Repeat("x", maxPlanOutputBytes), "x"}}
	_, err := New(store, provider, Config{MinEntriesToRun: 1}).Consolidate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid plan output") {
		t.Fatalf("over-limit error = %v", err)
	}
	if store.mutations() != 0 {
		t.Fatalf("over-limit output caused %d mutations", store.mutations())
	}
}

func TestPlannerReceivesExactFullValueAndDescription(t *testing.T) {
	value := strings.Repeat("v", 4096)
	description := strings.Repeat("d", 3072)
	provider := &captureProvider{reply: `{"supersessions":[]}`}
	store := newFakeStore(tool.MemoryEntry{Key: "a", Value: value, Description: description})
	consolidator := New(store, provider, Config{MinEntriesToRun: 1, MaxInputBytes: 16 * 1024})

	if _, err := consolidator.GeneratePlan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 || len(provider.requests[0].Messages) != 1 {
		t.Fatalf("requests = %#v", provider.requests)
	}
	content := provider.requests[0].Messages[0].Text
	if !strings.Contains(content, value) || !strings.Contains(content, description) || strings.Contains(content, "truncated") {
		t.Fatal("planner did not receive the complete value and description")
	}
}

func TestRotatingSelectionWrapsFairly(t *testing.T) {
	planner := &recordingPlanner{}
	consolidator := newWithPlanner(newFakeStore(entries("a", "b", "c")...), planner, Config{
		MinEntriesToRun: 1, MaxEntries: 2, MaxInputBytes: 4096,
	})
	for range 3 {
		if _, err := consolidator.GeneratePlan(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	got := []string{keys(planner.batches[0]), keys(planner.batches[1]), keys(planner.batches[2])}
	want := []string{"ab", "ca", "bc"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("batch %d = %q, want %q (all %v)", i, got[i], want[i], got)
		}
	}
}

func TestOversizedEntryDoesNotPinCursor(t *testing.T) {
	all := entries("a", "b", "c")
	all[0].Value = strings.Repeat("x", 4096)
	// This budget fits either small entry exactly enough, but never a.
	limit := len(renderEntries([]tool.MemoryEntry{all[1]}))
	planner := &recordingPlanner{}
	consolidator := newWithPlanner(newFakeStore(all...), planner, Config{
		MinEntriesToRun: 1, MaxEntries: 1, MaxInputBytes: limit,
	})
	for range 3 {
		if _, err := consolidator.GeneratePlan(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	got := []string{keys(planner.batches[0]), keys(planner.batches[1]), keys(planner.batches[2])}
	want := []string{"b", "c", "b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("batch %d = %q, want %q (all %v)", i, got[i], want[i], got)
		}
	}
}

func TestAggregateByteCapAdvancesAfterLastSelected(t *testing.T) {
	all := entries("a", "b", "c", "d")
	planner := &recordingPlanner{}
	consolidator := newWithPlanner(newFakeStore(all...), planner, Config{
		MinEntriesToRun: 1, MaxEntries: 4, MaxInputBytes: len(renderEntries(all[:2])),
	})
	for range 2 {
		if _, err := consolidator.GeneratePlan(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := []string{keys(planner.batches[0]), keys(planner.batches[1])}; got[0] != "ab" || got[1] != "cd" {
		t.Fatalf("aggregate-capped batches = %v, want [ab cd]", got)
	}
}

func TestAggregateInputDefaultAndOverride(t *testing.T) {
	defaults := withDefaults(Config{})
	if defaults.MaxInputBytes != 256*1024 {
		t.Fatalf("default MaxInputBytes = %d", defaults.MaxInputBytes)
	}
	const configured = 8192
	if got := withDefaults(Config{MaxInputBytes: configured}).MaxInputBytes; got != configured {
		t.Fatalf("configured MaxInputBytes = %d, want %d", got, configured)
	}
}

func TestAggregateInputLimitCanExcludeEveryEntry(t *testing.T) {
	planner := &recordingPlanner{}
	consolidator := newWithPlanner(newFakeStore(entries("a")...), planner, Config{
		MinEntriesToRun: 1, MaxInputBytes: len(entriesPrefix) + 2,
	})
	if _, err := consolidator.GeneratePlan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(planner.batches) != 0 {
		t.Fatalf("planner called with over-budget input: %#v", planner.batches)
	}
}

func TestConvergenceCandidatesBindExactInspectedRevision(t *testing.T) {
	ctx := context.Background()
	store := memmemory.New()
	entry := tool.MemoryEntry{Key: "project/a", Value: "complete value", Description: "complete description"}
	if err := store.RememberEntry(ctx, entry); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.Inspect(ctx, entry.Key)
	if err != nil || !found {
		t.Fatalf("inspect: found=%v err=%v", found, err)
	}
	planner := &recordingPlanner{}
	consolidator := newWithPlanner(store, planner, Config{MinEntriesToRun: 1})
	plan, err := consolidator.GeneratePlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	binding := plan.candidates[entry.Key]
	if binding.version != record.Current.Version {
		t.Fatalf("binding version = %#v, want %q", binding, record.Current.Version)
	}
	if binding.entry.Value != record.Current.Value || binding.entry.Description != record.Current.Description {
		t.Fatalf("binding did not retain exact inspected content: %#v", binding.entry)
	}
	if len(planner.batches) != 1 || planner.batches[0][0].Value != record.Current.Value || planner.batches[0][0].Description != record.Current.Description {
		t.Fatalf("planner input was not inspected content: %#v", planner.batches)
	}
}

type blockingPlanner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingPlanner) Plan(ctx context.Context, _ []tool.MemoryEntry) ([]supersession, error) {
	p.once.Do(func() { close(p.started) })
	select {
	case <-p.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestOperationsSerializeAndCancelledWaiterDoesNotConsumeGate(t *testing.T) {
	planner := &blockingPlanner{started: make(chan struct{}), release: make(chan struct{})}
	consolidator := newWithPlanner(newFakeStore(entries("a")...), planner, Config{MinEntriesToRun: 1, Timeout: time.Second})
	firstDone := make(chan error, 1)
	go func() {
		_, err := consolidator.GeneratePlan(context.Background())
		firstDone <- err
	}()
	<-planner.started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := consolidator.ApplyPlan(ctx, Plan{owner: consolidator}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error = %v", err)
	}
	close(planner.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first generation: %v", err)
	}
	if _, err := consolidator.GeneratePlan(context.Background()); err != nil {
		t.Fatalf("gate remained consumed: %v", err)
	}
}

func TestConsolidateDoesNotSelfDeadlock(t *testing.T) {
	consolidator := newWithPlanner(newFakeStore(entries("a")...), &recordingPlanner{}, Config{MinEntriesToRun: 1})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := consolidator.Consolidate(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunPeriodicallyStopsOnContext(t *testing.T) {
	consolidator := newWithPlanner(newFakeStore(), &recordingPlanner{}, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := consolidator.RunPeriodically(ctx, 5*time.Millisecond, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunPeriodically error = %v", err)
	}
}

func seedStore(t *testing.T, values map[string]tool.MemoryEntry) *atomicTestStore {
	t.Helper()
	store := &atomicTestStore{Store: memmemory.New()}
	for key, entry := range values {
		entry.Key = key
		if err := store.RememberEntry(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func generate(t *testing.T, store tool.MemoryStore, cfg Config, operations ...supersession) (*Consolidator, Plan) {
	t.Helper()
	cfg.MinEntriesToRun = 1
	consolidator := newWithPlanner(store, &recordingPlanner{ops: operations}, cfg)
	plan, err := consolidator.GeneratePlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return consolidator, plan
}

func TestApplyExactDuplicateRetiresSourceWithoutSurvivorWrite(t *testing.T) {
	entry := tool.MemoryEntry{Value: "byte-exact value", Description: "byte-exact description"}
	store := seedStore(t, map[string]tool.MemoryEntry{"a": entry, "b": entry})
	consolidator, plan := generate(t, store, Config{}, supersession{Survivor: "a", Superseded: []string{"b"}})

	report, err := consolidator.ApplyPlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if report != (Report{Planned: 1, Applied: 1, Kept: 1, Merged: 1}) {
		t.Fatalf("report = %+v", report)
	}
	survivor, found, err := store.Inspect(context.Background(), "a")
	if err != nil || !found || len(survivor.Revisions) != 1 || survivor.Current.Status != tool.MemoryStatusActive {
		t.Fatalf("survivor was rewritten: found=%v err=%v record=%+v", found, err, survivor)
	}
	source, found, err := store.Inspect(context.Background(), "b")
	if err != nil || !found || len(source.Revisions) != 2 || source.Current.Status != tool.MemoryStatusDeleted {
		t.Fatalf("source lifecycle = found=%v err=%v record=%+v", found, err, source)
	}
	if source.Revisions[0].Value != entry.Value || source.Revisions[0].Description != entry.Description {
		t.Fatalf("retirement lost source content: %+v", source.Revisions)
	}
	if source.Current.Writer != tool.MemoryWriterSystem || source.Current.Origin != tool.MemoryOriginConsolidation {
		t.Fatalf("retirement attribution = (%q, %q)", source.Current.Writer, source.Current.Origin)
	}
}

func TestApplyNonIdenticalSourceIsSkipped(t *testing.T) {
	store := seedStore(t, map[string]tool.MemoryEntry{
		"a": {Value: "same", Description: "description"},
		"b": {Value: "different", Description: "description"},
		"c": {Value: "same", Description: "different"},
	})
	consolidator, plan := generate(t, store, Config{}, supersession{Survivor: "a", Superseded: []string{"b", "c"}})
	report, err := consolidator.ApplyPlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if report.Planned != 2 || report.Skipped != 2 || report.Kept != 3 {
		t.Fatalf("report = %+v", report)
	}
	for _, key := range []string{"a", "b", "c"} {
		record, _, _ := store.Inspect(context.Background(), key)
		if record.Current.Status != tool.MemoryStatusActive || len(record.Revisions) != 1 {
			t.Fatalf("%s mutated: %+v", key, record)
		}
	}
}

func TestApplyRequiresByteIdenticalUnicodeAndDescription(t *testing.T) {
	for _, tc := range []struct {
		name     string
		survivor tool.MemoryEntry
		source   tool.MemoryEntry
	}{
		{name: "unicode normalization", survivor: tool.MemoryEntry{Value: "é", Description: "d"}, source: tool.MemoryEntry{Value: "e\u0301", Description: "d"}},
		{name: "description whitespace", survivor: tool.MemoryEntry{Value: "v", Description: "line"}, source: tool.MemoryEntry{Value: "v", Description: "line\t"}},
		{name: "description control", survivor: tool.MemoryEntry{Value: "v", Description: "line"}, source: tool.MemoryEntry{Value: "v", Description: "li\u0000ne"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := seedStore(t, map[string]tool.MemoryEntry{"a": tc.survivor, "b": tc.survivor})
			consolidator, plan := generate(t, store, Config{}, supersession{Survivor: "a", Superseded: []string{"b"}})
			binding := plan.candidates["b"]
			binding.entry = tc.source
			plan.candidates["b"] = binding
			report, err := consolidator.ApplyPlan(context.Background(), plan)
			if err != nil || report.Skipped != 1 || report.Applied != 0 {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			record, found, inspectErr := store.Inspect(context.Background(), "b")
			if inspectErr != nil || !found || record.Current.Status != tool.MemoryStatusActive {
				t.Fatalf("source changed: found=%v record=%+v err=%v", found, record, inspectErr)
			}
		})
	}
}

func TestBaseStoreReportsNormalizedRetirementsWithoutMutation(t *testing.T) {
	store := newFakeStore(
		tool.MemoryEntry{Key: "a", Value: "v", Description: "d"},
		tool.MemoryEntry{Key: "b", Value: "v", Description: "d"},
	)
	consolidator, plan := generate(t, store, Config{}, supersession{Survivor: " a ", Superseded: []string{" b ", "missing"}})
	report, err := consolidator.ApplyPlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if report.Planned != 1 || report.Skipped != 1 || store.mutations() != 0 {
		t.Fatalf("report=%+v mutations=%d", report, store.mutations())
	}
}

func TestApplyStaleSurvivorConflictsWholeOperationAndStaleSourceOnlyItself(t *testing.T) {
	entry := tool.MemoryEntry{Value: "v", Description: "d"}
	store := seedStore(t, map[string]tool.MemoryEntry{"a": entry, "b": entry, "c": entry})
	consolidator, plan := generate(t, store, Config{}, supersession{Survivor: "a", Superseded: []string{"b", "c"}})
	if err := store.RememberEntry(context.Background(), tool.MemoryEntry{Key: "a", Value: "v", Description: "d"}); err != nil {
		t.Fatal(err)
	}
	report, err := consolidator.ApplyPlan(context.Background(), plan)
	if err != nil || report.Conflicted != 2 {
		t.Fatalf("stale survivor report=%+v err=%v", report, err)
	}

	store = seedStore(t, map[string]tool.MemoryEntry{"a": entry, "b": entry, "c": entry})
	consolidator, plan = generate(t, store, Config{}, supersession{Survivor: "a", Superseded: []string{"b", "c"}})
	if err := store.RememberEntry(context.Background(), tool.MemoryEntry{Key: "b", Value: "v", Description: "d"}); err != nil {
		t.Fatal(err)
	}
	report, err = consolidator.ApplyPlan(context.Background(), plan)
	if err != nil || report.Applied != 1 || report.Conflicted != 1 {
		t.Fatalf("stale source report=%+v err=%v", report, err)
	}
}

type survivorRaceStore struct {
	*atomicTestStore
	mutated bool
}

func (s *survivorRaceStore) RetireDuplicate(ctx context.Context, survivorKey string, survivorVersion tool.MemoryVersion, sourceKey string, sourceVersion tool.MemoryVersion) (tool.MemoryRecord, error) {
	if !s.mutated {
		s.mutated = true
		if err := s.RememberEntry(ctx, tool.MemoryEntry{Key: survivorKey, Value: "changed", Description: "d"}); err != nil {
			return tool.MemoryRecord{}, err
		}
	}
	return s.atomicTestStore.RetireDuplicate(ctx, survivorKey, survivorVersion, sourceKey, sourceVersion)
}

func TestAtomicRetirementDetectsSurvivorChangeAtMutationBoundary(t *testing.T) {
	entry := tool.MemoryEntry{Value: "v", Description: "d"}
	store := &survivorRaceStore{atomicTestStore: seedStore(t, map[string]tool.MemoryEntry{"a": entry, "b": entry})}
	consolidator, plan := generate(t, store, Config{}, supersession{Survivor: "a", Superseded: []string{"b"}})
	report, err := consolidator.ApplyPlan(context.Background(), plan)
	if err != nil || report.Conflicted != 1 || report.Applied != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	source, found, inspectErr := store.Inspect(context.Background(), "b")
	if inspectErr != nil || !found || source.Current.Status != tool.MemoryStatusActive {
		t.Fatalf("source retired after survivor race: found=%v record=%+v err=%v", found, source, inspectErr)
	}
}

type failingConvergenceStore struct {
	*atomicTestStore
	failKey string
	active  int
	max     int
	mu      sync.Mutex
	delay   time.Duration
}

func (s *failingConvergenceStore) RetireDuplicate(ctx context.Context, survivorKey string, survivorVersion tool.MemoryVersion, sourceKey string, sourceVersion tool.MemoryVersion) (tool.MemoryRecord, error) {
	s.mu.Lock()
	s.active++
	if s.active > s.max {
		s.max = s.active
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return tool.MemoryRecord{}, ctx.Err()
		}
	}
	if sourceKey == s.failKey {
		return tool.MemoryRecord{}, errors.New("injected retirement failure")
	}
	return s.atomicTestStore.RetireDuplicate(ctx, survivorKey, survivorVersion, sourceKey, sourceVersion)
}

func TestApplyContinuesAfterConflictAndFailure(t *testing.T) {
	entry := tool.MemoryEntry{Value: "v", Description: "d"}
	base := seedStore(t, map[string]tool.MemoryEntry{"a": entry, "b": entry, "c": entry, "d": entry})
	store := &failingConvergenceStore{atomicTestStore: base, failKey: "d"}
	consolidator, plan := generate(t, store, Config{}, supersession{Survivor: "a", Superseded: []string{"b", "c", "d"}})
	if err := base.RememberEntry(context.Background(), tool.MemoryEntry{Key: "c", Value: "v", Description: "d"}); err != nil {
		t.Fatal(err)
	}
	report, err := consolidator.ApplyPlan(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "injected retirement failure") {
		t.Fatalf("error = %v", err)
	}
	if report.Planned != 3 || report.Applied != 1 || report.Conflicted != 1 || report.Failed != 1 || report.Merged != 1 || report.Forgotten != 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.Planned != report.Applied+report.Conflicted+report.Skipped+report.Failed {
		t.Fatalf("accounting identity failed: %+v", report)
	}
}

func TestApplyContinuesWithLaterOperationAfterEarlierFailure(t *testing.T) {
	entry := tool.MemoryEntry{Value: "v", Description: "d"}
	base := seedStore(t, map[string]tool.MemoryEntry{"a": entry, "b": entry, "c": entry, "d": entry})
	store := &failingConvergenceStore{atomicTestStore: base, failKey: "b"}
	consolidator, plan := generate(t, store, Config{},
		supersession{Survivor: "a", Superseded: []string{"b"}},
		supersession{Survivor: "c", Superseded: []string{"d"}},
	)
	report, err := consolidator.ApplyPlan(context.Background(), plan)
	if err == nil || report.Planned != 2 || report.Applied != 1 || report.Failed != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if report.Planned != report.Applied+report.Conflicted+report.Skipped+report.Failed {
		t.Fatalf("accounting identity failed: %+v", report)
	}
	record, _, inspectErr := base.Inspect(context.Background(), "d")
	if inspectErr != nil || record.Current.Status != tool.MemoryStatusDeleted {
		t.Fatalf("later operation did not continue: record=%+v err=%v", record, inspectErr)
	}
}

func TestNormalizationAndMaxForgetsSemantics(t *testing.T) {
	entry := tool.MemoryEntry{Value: "v", Description: "d"}
	values := map[string]tool.MemoryEntry{"a": entry, "b": entry, "c": entry, "d": entry}
	operations := []supersession{
		{Survivor: " a ", Superseded: []string{" b ", "b", "missing", "a", "c"}},
		{Survivor: "b", Superseded: []string{"d"}},
		{Survivor: "d", Superseded: []string{"a"}},
		{Survivor: "a", Superseded: []string{"d"}},
	}
	for _, tc := range []struct {
		name       string
		maxForgets int
		planned    int
		applied    int
		skipped    int
	}{
		{name: "explicit cap", maxForgets: 2, planned: 2, applied: 2},
		{name: "negative disables", maxForgets: -1, planned: 3, skipped: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := seedStore(t, values)
			consolidator, plan := generate(t, store, Config{MaxForgets: tc.maxForgets}, operations...)
			report, err := consolidator.ApplyPlan(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if report.Planned != tc.planned || report.Applied != tc.applied || report.Skipped != tc.skipped {
				t.Fatalf("report = %+v", report)
			}
		})
	}

	many := make(map[string]tool.MemoryEntry)
	many["a"] = entry
	sources := make([]string, 12)
	for i := range sources {
		sources[i] = string(rune('b' + i))
		many[sources[i]] = entry
	}
	store := seedStore(t, many)
	consolidator, plan := generate(t, store, Config{}, supersession{Survivor: "a", Superseded: sources})
	report, err := consolidator.ApplyPlan(context.Background(), plan)
	if err != nil || report.Planned != defaultMaxForgets || report.Applied != defaultMaxForgets {
		t.Fatalf("default cap report=%+v err=%v", report, err)
	}
}

func TestRunPeriodicallyWithReportReportsSuccess(t *testing.T) {
	consolidator := newWithPlanner(newFakeStore(entries("a")...), &recordingPlanner{}, Config{MinEntriesToRun: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan Report, 1)
	done := make(chan error, 1)
	go func() {
		done <- consolidator.RunPeriodicallyWithReport(ctx, time.Millisecond, func(report Report, _ error) {
			select {
			case got <- report:
			default:
			}
			cancel()
		})
	}()
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("report callback was not called")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("periodic error = %v", err)
	}
}

func TestRunPeriodicallyWithReportReportsPartialFailure(t *testing.T) {
	entry := tool.MemoryEntry{Value: "v", Description: "d"}
	store := &failingConvergenceStore{
		atomicTestStore: seedStore(t, map[string]tool.MemoryEntry{"a": entry, "b": entry}),
		failKey:         "b",
	}
	consolidator := newWithPlanner(store, &recordingPlanner{ops: []supersession{{Survivor: "a", Superseded: []string{"b"}}}}, Config{MinEntriesToRun: 1})
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan struct {
		report Report
		err    error
	}, 1)
	done := make(chan error, 1)
	go func() {
		done <- consolidator.RunPeriodicallyWithReport(ctx, time.Millisecond, func(report Report, err error) {
			got <- struct {
				report Report
				err    error
			}{report: report, err: err}
			cancel()
		})
	}()
	select {
	case result := <-got:
		if result.err == nil || result.report.Planned != 1 || result.report.Failed != 1 {
			t.Fatalf("callback report=%+v err=%v", result.report, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("partial report callback was not called")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("periodic error = %v", err)
	}
}

func TestConcurrentConsolidationApplyRemainsSerialized(t *testing.T) {
	entry := tool.MemoryEntry{Value: "v", Description: "d"}
	base := seedStore(t, map[string]tool.MemoryEntry{"a": entry, "b": entry, "c": entry})
	store := &failingConvergenceStore{atomicTestStore: base, delay: 20 * time.Millisecond}
	consolidator, plan := generate(t, store, Config{}, supersession{Survivor: "a", Superseded: []string{"b", "c"}})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = consolidator.ApplyPlan(context.Background(), plan)
		}()
	}
	wg.Wait()
	store.mu.Lock()
	maxActive := store.max
	store.mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("overlapping mutations = %d, want 1", maxActive)
	}
}

type captureProvider struct {
	reply    string
	chunks   []string
	requests []port.LLMRequest
}

func (*captureProvider) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }
func (p *captureProvider) Stream(_ context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.requests = append(p.requests, req)
	return func(yield func(port.Chunk, error) bool) {
		chunks := p.chunks
		if chunks == nil {
			chunks = []string{p.reply}
		}
		for _, chunk := range chunks {
			if !yield(port.Chunk{Kind: port.ChunkText, Text: chunk}, nil) {
				return
			}
		}
	}, nil
}
