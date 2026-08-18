package app

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/dream"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const duplicateDreamPlan = `{"exact_duplicates":[{"survivor":"a","superseded":["b"],"reason":"same"}],"synthesized_replacements":[]}`

func dreamStore(t *testing.T) *memory.Store {
	t.Helper()
	store, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []tool.MemoryEntry{
		{Key: "a", Value: "same", Description: "same"},
		{Key: "b", Value: "same", Description: "same"},
	} {
		if err := store.RememberEntry(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func dreamConsolidator(store tool.MemoryStore) *dream.Consolidator {
	turns := make([]mockllm.Turn, 16)
	for i := range turns {
		turns[i] = mockllm.TextTurn(duplicateDreamPlan)
	}
	return dream.New(store, mockllm.New(turns...), dream.Config{Model: "test", MinEntriesToRun: 1})
}

func TestDreamReviewGenerationIsOpaqueNonMutatingAndAppliesRetainedPlan(t *testing.T) {
	store := dreamStore(t)
	coordinator := newDreamReviewCoordinator(dreamReviewConfig{targets: map[server.DreamTarget]*dream.Consolidator{
		server.DreamTargetProjectMemory: dreamConsolidator(store),
	}})

	review, err := coordinator.Generate(context.Background(), server.DreamTargetProjectMemory)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(review.ID) {
		t.Fatalf("non-opaque id %q", review.ID)
	}
	if _, found, err := store.Recall(context.Background(), "b"); err != nil || !found {
		t.Fatalf("generation mutated source: found=%v err=%v", found, err)
	}
	if review.PlannedOperations != 1 || review.PlannedSourceCount != 1 || len(review.Operations) != 1 {
		t.Fatalf("review counts/projection = %+v", review)
	}
	// Detached projection changes cannot alter the authoritative retained plan.
	review.Operations[0].Sources[0].Key = "not-b"
	receipt, err := coordinator.Decide(context.Background(), review.ID, server.DreamDecisionApply)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Planned != 1 || receipt.Applied != 1 || receipt.Conflicted+receipt.Skipped+receipt.Failed != 0 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if _, found, err := store.Recall(context.Background(), "b"); err != nil || found {
		t.Fatalf("retained plan did not retire b: found=%v err=%v", found, err)
	}
	coordinator.mu.Lock()
	record := coordinator.records[review.ID]
	if !reflect.DeepEqual(record.plan, dream.Plan{}) || !reflect.DeepEqual(record.review, server.DreamReview{}) {
		t.Fatalf("terminal record retained content: %#v", record)
	}
	coordinator.mu.Unlock()

	retry, err := coordinator.Decide(context.Background(), review.ID, server.DreamDecisionApply)
	if err != nil || retry != receipt {
		t.Fatalf("same-decision retry = (%+v, %v), want (%+v, nil)", retry, err, receipt)
	}
	if _, err := coordinator.Decide(context.Background(), review.ID, server.DreamDecisionDismiss); !errors.Is(err, server.ErrDreamTerminalConflict) {
		t.Fatalf("opposite decision = %v", err)
	}
}

func TestDreamReviewPerTargetCapacityIsIndependent(t *testing.T) {
	ids := []string{"project", "user", "next"}
	idIndex := 0
	store := dreamStore(t)
	consolidator := dreamConsolidator(store)
	coordinator := newDreamReviewCoordinator(dreamReviewConfig{
		targets: map[server.DreamTarget]*dream.Consolidator{
			server.DreamTargetProjectMemory: consolidator,
			server.DreamTargetUserModel:     consolidator,
		},
		newID:      func() (string, error) { id := ids[idIndex]; idIndex++; return id, nil },
		maxRecords: 10, maxPending: 1,
	})
	if _, err := coordinator.Generate(context.Background(), server.DreamTargetProjectMemory); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Generate(context.Background(), server.DreamTargetProjectMemory); !errors.Is(err, server.ErrDreamCapacity) {
		t.Fatalf("same-target capacity = %v", err)
	}
	if _, err := coordinator.Generate(context.Background(), server.DreamTargetUserModel); err != nil {
		t.Fatalf("other target was incorrectly capacity-blocked: %v", err)
	}
}

func TestDreamReviewGlobalCapacityAndTTLCleanup(t *testing.T) {
	now := time.Unix(100, 0)
	ids := []string{"one", "two"}
	idIndex := 0
	store := dreamStore(t)
	coordinator := newDreamReviewCoordinator(dreamReviewConfig{
		targets: map[server.DreamTarget]*dream.Consolidator{server.DreamTargetProjectMemory: dreamConsolidator(store)},
		now:     func() time.Time { return now }, newID: func() (string, error) { id := ids[idIndex]; idIndex++; return id, nil },
		ttl: time.Minute, maxRecords: 1, maxPending: 1,
	})
	review, err := coordinator.Generate(context.Background(), server.DreamTargetProjectMemory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Generate(context.Background(), server.DreamTargetProjectMemory); !errors.Is(err, server.ErrDreamCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
	receipt, err := coordinator.Decide(context.Background(), review.ID, server.DreamDecisionDismiss)
	if err != nil || receipt.Planned != 1 || receipt.Skipped != 1 || receipt.Applied != 0 {
		t.Fatalf("dismiss = (%+v, %v)", receipt, err)
	}
	if _, found, _ := store.Recall(context.Background(), "b"); !found {
		t.Fatal("dismiss mutated source")
	}
	coordinator.mu.Lock()
	record := coordinator.records[review.ID]
	if !reflect.DeepEqual(record.plan, dream.Plan{}) || !reflect.DeepEqual(record.review, server.DreamReview{}) {
		coordinator.mu.Unlock()
		t.Fatalf("dismiss retained content: %#v", record)
	}
	coordinator.mu.Unlock()
	retry, err := coordinator.Decide(context.Background(), review.ID, server.DreamDecisionDismiss)
	if err != nil || retry != receipt {
		t.Fatalf("dismiss retry = (%+v, %v), want %+v", retry, err, receipt)
	}

	restarted := newDreamReviewCoordinator(dreamReviewConfig{targets: coordinator.targets})
	if _, err := restarted.Decide(context.Background(), review.ID, server.DreamDecisionDismiss); !errors.Is(err, server.ErrDreamNotFound) {
		t.Fatalf("restart error = %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := coordinator.Decide(context.Background(), review.ID, server.DreamDecisionDismiss); !errors.Is(err, server.ErrDreamNotFound) {
		t.Fatalf("expiry error = %v", err)
	}
}

type blockingReviewedStore struct {
	*memory.Store
	entered chan struct{}
	release chan struct{}
}

func (s *blockingReviewedStore) RetireDuplicate(ctx context.Context, survivor string, survivorVersion tool.MemoryVersion, source string, sourceVersion tool.MemoryVersion) (tool.MemoryRecord, error) {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return tool.MemoryRecord{}, ctx.Err()
	}
	return s.Store.RetireDuplicate(ctx, survivor, survivorVersion, source, sourceVersion)
}

func TestDreamReviewGateCancellationTerminalizesCompleteIdempotentReceipt(t *testing.T) {
	store := &blockingReviewedStore{Store: dreamStore(t), entered: make(chan struct{}), release: make(chan struct{})}
	coordinator := newDreamReviewCoordinator(dreamReviewConfig{targets: map[server.DreamTarget]*dream.Consolidator{
		server.DreamTargetProjectMemory: dreamConsolidator(store),
	}})
	first, err := coordinator.Generate(context.Background(), server.DreamTargetProjectMemory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.Generate(context.Background(), server.DreamTargetProjectMemory)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, applyErr := coordinator.Decide(context.Background(), first.ID, server.DreamDecisionApply)
		firstDone <- applyErr
	}()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("first apply did not enter store")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	receipt, err := coordinator.Decide(ctx, second.ID, server.DreamDecisionApply)
	if err != nil {
		t.Fatalf("fully accounted cancellation returned transport error: %v", err)
	}
	if receipt.Planned != 1 || receipt.Failed != 1 || !completeDreamReceipt(receipt) {
		t.Fatalf("cancelled receipt = %+v", receipt)
	}
	retry, err := coordinator.Decide(context.Background(), second.ID, server.DreamDecisionApply)
	if err != nil || retry != receipt {
		t.Fatalf("idempotent retry = (%+v, %v), want %+v", retry, err, receipt)
	}
	close(store.release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("first apply did not finish")
	}
}

func TestDreamReviewConcurrentApplyConflicts(t *testing.T) {
	store := &blockingReviewedStore{Store: dreamStore(t), entered: make(chan struct{}), release: make(chan struct{})}
	coordinator := newDreamReviewCoordinator(dreamReviewConfig{targets: map[server.DreamTarget]*dream.Consolidator{
		server.DreamTargetProjectMemory: dreamConsolidator(store),
	}})
	review, err := coordinator.Generate(context.Background(), server.DreamTargetProjectMemory)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, applyErr := coordinator.Decide(context.Background(), review.ID, server.DreamDecisionApply)
		done <- applyErr
	}()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("apply did not enter store")
	}
	if _, err := coordinator.Decide(context.Background(), review.ID, server.DreamDecisionApply); !errors.Is(err, server.ErrDreamInProgress) {
		t.Fatalf("concurrent same decision = %v", err)
	}
	if _, err := coordinator.Decide(context.Background(), review.ID, server.DreamDecisionDismiss); !errors.Is(err, server.ErrDreamConflict) {
		t.Fatalf("concurrent opposite decision = %v", err)
	}
	close(store.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("apply did not finish")
	}
}

type failingReviewedStore struct{ *memory.Store }

func (*failingReviewedStore) RetireDuplicate(context.Context, string, tool.MemoryVersion, string, tool.MemoryVersion) (tool.MemoryRecord, error) {
	return tool.MemoryRecord{}, errors.New("SECRET memory content")
}

func TestDreamReviewGenerationFailureStoresNothingAndSanitizesError(t *testing.T) {
	store := dreamStore(t)
	bad := dream.New(store, mockllm.New(mockllm.TextTurn(`SECRET provider content`)), dream.Config{Model: "test", MinEntriesToRun: 1})
	coordinator := newDreamReviewCoordinator(dreamReviewConfig{targets: map[server.DreamTarget]*dream.Consolidator{
		server.DreamTargetProjectMemory: bad,
	}})
	_, err := coordinator.Generate(context.Background(), server.DreamTargetProjectMemory)
	if !errors.Is(err, server.ErrDreamGenerateFailed) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unsanitized generation error = %v", err)
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.records) != 0 {
		t.Fatalf("generation failure retained records: %#v", coordinator.records)
	}
}

func TestDreamReviewPartialReceiptAndSanitizedError(t *testing.T) {
	store := &failingReviewedStore{Store: dreamStore(t)}
	coordinator := newDreamReviewCoordinator(dreamReviewConfig{targets: map[server.DreamTarget]*dream.Consolidator{
		server.DreamTargetProjectMemory: dreamConsolidator(store),
	}})
	review, err := coordinator.Generate(context.Background(), server.DreamTargetProjectMemory)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := coordinator.Decide(context.Background(), review.ID, server.DreamDecisionApply)
	if err != nil {
		t.Fatalf("accounted apply error = %v", err)
	}
	if receipt.Planned != 1 || receipt.Failed != 1 || receipt.Applied != 0 || !completeDreamReceipt(receipt) {
		t.Fatalf("partial receipt = %+v", receipt)
	}
	retry, retryErr := coordinator.Decide(context.Background(), review.ID, server.DreamDecisionApply)
	if retry != receipt || retryErr != nil {
		t.Fatalf("partial retry = (%+v, %v)", retry, retryErr)
	}
}

func TestDreamReviewTargetIsolationAndCapabilities(t *testing.T) {
	projectStore, userStore := dreamStore(t), dreamStore(t)
	assets := catalogAssets{
		memStore: projectStore, userModelStore: userStore,
		memoryDream: dreamConsolidator(projectStore), userModelDream: dreamConsolidator(userStore),
	}
	reviewer, caps := buildDreamReview(Config{}, assets, true)
	if reviewer == nil || !caps.Generate || !caps.Decide || !caps.Targets[server.DreamTargetProjectMemory].Generate || !caps.Targets[server.DreamTargetUserModel].Generate {
		t.Fatalf("available caps = %+v", caps)
	}
	review, err := reviewer.Generate(context.Background(), server.DreamTargetUserModel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewer.Decide(context.Background(), review.ID, server.DreamDecisionApply); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := userStore.Recall(context.Background(), "b"); found {
		t.Fatal("user-model target was not mutated")
	}
	if _, found, _ := projectStore.Recall(context.Background(), "b"); !found {
		t.Fatal("user-model decision crossed into project target")
	}

	for name, tc := range map[string]struct {
		cfg      Config
		assets   catalogAssets
		provider bool
	}{
		"ownership":  {cfg: Config{OwnershipEnforced: true}, assets: assets, provider: true},
		"provider":   {assets: assets, provider: false},
		"store":      {assets: catalogAssets{}, provider: true},
		"capability": {assets: catalogAssets{memStore: &memory.NamespacedStore{}}, provider: true},
	} {
		t.Run(name, func(t *testing.T) {
			reviewer, caps := buildDreamReview(tc.cfg, tc.assets, tc.provider)
			if reviewer != nil || caps.Generate || caps.Decide || caps.UnavailableReason == "" {
				t.Fatalf("unavailable = reviewer %T caps %+v", reviewer, caps)
			}
		})
	}
}
