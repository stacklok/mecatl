package skillstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/skillconformance"
	"github.com/stacklok/mecatl/engine/adapter/skilllifecycle"
	"github.com/stacklok/mecatl/engine/adapter/skillvalidation"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/skillstore"
)

func TestConformance(t *testing.T) {
	factory := func(t *testing.T) learning.SkillRepository {
		store, err := skillstore.New(filepath.Join(t.TempDir(), "skills"))
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	skillconformance.Run(t, factory)
	skillconformance.RunValidatedActivation(t, factory)
}

func TestLazyStartupAndReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	store, err := skillstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.List(context.Background(), partition(), learning.SkillList{})
	if err != nil || len(page.Versions) != 0 {
		t.Fatalf("empty list=%#v err=%v", page, err)
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("empty startup created storage: %v", err)
	}
	draft := create(t, store, "reopen", "Persist this body.", "one")
	reopened, err := skillstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := reopened.Get(context.Background(), partition(), "agent-a", draft.ID, draft.Version)
	if err != nil || !found || got.Bundle != draft.Bundle || got.Revision != draft.Revision {
		t.Fatalf("reopen=%#v found=%v err=%v", got, found, err)
	}
}

func TestSimilarStageHintSurvivesReopenAndRetryCannotAutoActivate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	store, err := skillstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	input := learning.SkillDraftInput{
		Partition: partition(), OwnerAgent: "agent-a",
		Bundle:     bundle("reviews", "Inspect focused changes and report actionable findings before merging safely."),
		Provenance: provenance("similar"),
	}
	candidate := skilllifecycle.Candidate{
		Draft: input, Mode: learning.Review, Automatic: true,
		Inventory: []learning.SkillInventoryItem{{Name: "review", AgentOwned: true, OwnerAgent: "agent-a", Bundle: bundle("review", "Inspect focused changes and report actionable findings before merging safely.")}},
	}
	pipeline := skilllifecycle.Pipeline{Repository: store, Validator: skillvalidation.Validator{}, Evaluator: passSkillEvaluator{}}
	first, err := pipeline.Process(context.Background(), candidate)
	if err != nil || first.State != learning.SkillStaged {
		t.Fatalf("initial process=%+v err=%v", first, err)
	}

	reopened, err := skillstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	published := &countingSkillPublisher{}
	pipeline.Repository, pipeline.Publisher = reopened, published
	candidate.Mode = learning.Auto
	second, err := pipeline.Process(context.Background(), candidate)
	if err != nil || second.State != learning.SkillStaged || second.Published || published.calls != 0 {
		t.Fatalf("retry=%+v publishes=%d err=%v", second, published.calls, err)
	}
	got, found, err := reopened.Get(context.Background(), partition(), "agent-a", second.SkillID, second.Version)
	if err != nil || !found || got.Disposition != learning.ValidationSimilarStageHint || got.Provenance.ValidationDisposition != learning.ValidationSimilarStageHint {
		t.Fatalf("reopened disposition=%q provenance=%q found=%v err=%v", got.Disposition, got.Provenance.ValidationDisposition, found, err)
	}
}

type passSkillEvaluator struct{}

func (passSkillEvaluator) Evaluate(context.Context, learning.SkillEvaluationRequest) (learning.SkillEvaluation, error) {
	return learning.SkillEvaluation{Verdict: learning.EvaluationPass, FixtureIDs: []string{"fixture"}}, nil
}

type countingSkillPublisher struct{ calls int }

func (p *countingSkillPublisher) Publish(context.Context) error { p.calls++; return nil }

func TestConcurrentInstancesCASAndNoLostCreate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	first, _ := skillstore.New(root)
	second, _ := skillstore.New(root)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, store := range []*skillstore.Store{first, second} {
		wg.Add(1)
		go func(i int, store *skillstore.Store) {
			defer wg.Done()
			_, err := store.CreateDraft(ctx, partition(), "agent-a", bundle("skill-"+string(rune('a'+i)), "Distinct body."), provenance(string(rune('a'+i))))
			errs <- err
		}(i, store)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := first.List(ctx, partition(), learning.SkillList{})
	if err != nil || len(page.Versions) != 2 {
		t.Fatalf("multi-instance creates=%d err=%v", len(page.Versions), err)
	}

	draft := create(t, first, "cas", "Concurrent transition.", "cas")
	evaluated, err := first.RecordEvaluation(ctx, partition(), "agent-a", draft.ID, draft.Version, draft.Revision, evaluation())
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, store := range []*skillstore.Store{first, second} {
		wg.Add(1)
		go func(store *skillstore.Store) {
			defer wg.Done()
			_, stageErr := store.Stage(ctx, partition(), "agent-a", evaluated.ID, evaluated.Version, evaluated.Revision)
			results <- stageErr
		}(store)
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for result := range results {
		if result == nil {
			wins++
		} else if errors.Is(result, learning.ErrSkillConflict) {
			conflicts++
		} else {
			t.Fatal(result)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
}

func TestImmutableVersionAndOrphanRecovery(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	store, _ := skillstore.New(root)
	draft := create(t, store, "immutable", "Original body.", "one")
	path := filepath.Join(root, "versions", string(draft.Version), "SKILL.md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateDraft(context.Background(), partition(), "agent-a", draft.Bundle, provenance("two")); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("exact duplicate rewrote immutable content")
	}

	orphan := filepath.Join(root, "versions", "skill-version-00000000000000000000000000000000")
	if err = os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(orphan, "SKILL.md"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, _ := skillstore.New(root)
	if _, _, err = reopened.Get(context.Background(), partition(), "agent-a", draft.ID, draft.Version); err != nil {
		t.Fatalf("unreferenced crash orphan poisoned reopen: %v", err)
	}
}

func TestPartitionEncodingDoesNotCollideAndCrashTempsAreIgnored(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	store, err := skillstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	firstPartition := learning.SkillPartition{Principal: "a", Project: "bc"}
	secondPartition := learning.SkillPartition{Principal: "ab", Project: "c"}
	first, err := store.CreateDraft(context.Background(), firstPartition, "agent-a", bundle("partitioned", "First partition body."), provenance("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateDraft(context.Background(), secondPartition, "agent-a", bundle("partitioned", "Second partition body."), provenance("second"))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.Version == second.Version {
		t.Fatalf("partition records collided: first=%#v second=%#v", first, second)
	}
	if err = os.WriteFile(filepath.Join(root, ".skillstore-crash.tmp"), []byte("partial manifest"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := skillstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		partition learning.SkillPartition
		version   learning.SkillVersion
	}{{firstPartition, first}, {secondPartition, second}} {
		if _, found, getErr := reopened.Get(context.Background(), check.partition, "agent-a", check.version.ID, check.version.Version); getErr != nil || !found {
			t.Fatalf("reopen partition=%#v found=%v err=%v", check.partition, found, getErr)
		}
	}
}

func TestRejectsSymlinksAndPathInputs(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(base, "root-link")
	if err := os.Symlink(target, rootLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := skillstore.New(rootLink); err == nil {
		t.Fatal("accepted symlink repository root")
	}
	ancestorLink := filepath.Join(base, "ancestor-link")
	if err := os.Symlink(target, ancestorLink); err != nil {
		t.Fatal(err)
	}
	storeUnderLinkedAncestor, err := skillstore.New(filepath.Join(ancestorLink, "skills"))
	if err != nil {
		t.Fatalf("rejected symlinked ancestor: %v", err)
	}
	if _, err := storeUnderLinkedAncestor.CreateDraft(context.Background(), partition(), "agent-a", bundle("under-ancestor", "Safe body."), provenance("ancestor")); err != nil {
		t.Fatalf("create under symlinked ancestor: %v", err)
	}

	root := filepath.Join(base, "skills")
	store, _ := skillstore.New(root)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "versions")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDraft(context.Background(), partition(), "agent-a", bundle("escape", "Safe body."), provenance("escape")); err == nil {
		t.Fatal("accepted symlink versions directory")
	}
	if _, err := store.CreateDraft(context.Background(), partition(), "agent-a", bundle("../escape", "Safe body."), provenance("path")); !errors.Is(err, learning.ErrInvalidSkill) {
		t.Fatalf("path name error=%v", err)
	}
}

func TestDurableReceiptIndexPagesHistoricalVersionsAfterReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	store, err := skillstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first := create(t, store, "history", "First body.", "first")
	first, err = store.RecordEvaluation(ctx, partition(), "agent-a", first.ID, first.Version, first.Revision, evaluation())
	if err != nil {
		t.Fatal(err)
	}
	first, err = store.Stage(ctx, partition(), "agent-a", first.ID, first.Version, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Activate(ctx, partition(), "agent-a", first.ID, first.Version, first.Revision); err != nil {
		t.Fatal(err)
	}
	second := create(t, store, "history", "Second body.", "second")
	second, err = store.RecordEvaluation(ctx, partition(), "agent-a", second.ID, second.Version, second.Revision, evaluation())
	if err != nil {
		t.Fatal(err)
	}
	second, err = store.Stage(ctx, partition(), "agent-a", second.ID, second.Version, second.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Activate(ctx, partition(), "agent-a", second.ID, second.Version, second.Revision); err != nil {
		t.Fatal(err)
	}

	reopened, err := skillstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []learning.SkillReceiptRecord
	cursor := ""
	for {
		page, pageErr := reopened.ListSkillReceipts(ctx, partition(), learning.SkillReceiptList{After: cursor, Limit: 2})
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		if len(page.Records) > 2 {
			t.Fatalf("oversized page: %d", len(page.Records))
		}
		got = append(got, page.Records...)
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if len(got) != 7 {
		t.Fatalf("receipts=%d, want 7: %#v", len(got), got)
	}
	seenFirst := false
	for _, record := range got {
		if record.Version == first.Version {
			seenFirst = true
		}
	}
	if !seenFirst {
		t.Fatal("historical-version receipts were lost")
	}
	if _, err = reopened.ListSkillReceipts(ctx, partition(), learning.SkillReceiptList{After: "invalid", Limit: 2}); !errors.Is(err, learning.ErrSkillCursor) {
		t.Fatalf("invalid cursor error=%v", err)
	}
}

func partition() learning.SkillPartition {
	return learning.SkillPartition{Principal: "issuer\x00subject", Project: "project"}
}

func provenance(id string) learning.SkillProvenance {
	return learning.SkillProvenance{
		ProposalIDs:  []learning.ProposalID{learning.ProposalID("proposal-" + id)},
		EvidenceRefs: []learning.EvidenceRef{{SessionID: session.SessionID("session-" + id), Locator: learning.EvidenceMessage, Digest: strings.Repeat("a", 64)}},
	}
}

func bundle(name, body string) learning.SkillBundle {
	return learning.SkillBundle{Name: name, Description: "Reusable " + name + " procedure", Body: body}
}

func create(t *testing.T, store learning.SkillRepository, name, body, id string) learning.SkillVersion {
	t.Helper()
	value, err := store.CreateDraft(context.Background(), partition(), "agent-a", bundle(name, body), provenance(id))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func evaluation() learning.SkillEvaluation {
	return learning.SkillEvaluation{Verdict: learning.EvaluationPass, FixtureIDs: []string{"fixture"}, Baseline: "old", Treatment: "new", Reason: "pass"}
}
