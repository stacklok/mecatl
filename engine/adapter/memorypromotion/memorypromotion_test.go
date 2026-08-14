package memorypromotion_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/memorypromotion"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func fixture() (learning.ProposalPartition, learning.Candidate) {
	return learning.ProposalPartition{Principal: "i\x00s", Project: "p"}, learning.Candidate{Kind: learning.CandidateProjectFact, Key: "project/fact", Value: "Use task test.", Description: "Test command", Evidence: []learning.EvidenceRef{{SessionID: session.SessionID("s"), Locator: learning.EvidenceMessage, Ordinal: 0, Digest: strings.Repeat("b", 64)}}}
}
func TestPromoteReconcileUndo(t *testing.T) {
	ctx := context.Background()
	repo := memproposal.New()
	mem := memmemory.New()
	p, c := fixture()
	rs, e := repo.StageBatch(ctx, p, strings.Repeat("a", 64), []learning.Candidate{c}, nil)
	if e != nil {
		t.Fatal(e)
	}
	pr := memorypromotion.Promoter{Proposals: repo, Memory: mem}
	done, e := pr.Process(ctx, p, rs[0].ID, rs[0].Version, memorypromotion.PolicyInput{Mode: learning.Auto, TrustedProject: true})
	if e != nil || done.Status != learning.ProposalPromoted {
		t.Fatalf("promote %#v %v", done, e)
	}
	mr, _, _ := mem.Inspect(ctx, c.Key)
	if mr.Current.Source.ProposalID != string(rs[0].ID) {
		t.Fatal("missing linkage")
	}
	undone, e := pr.Undo(ctx, p, rs[0].ID, done.Version)
	if e != nil || undone.Status != learning.ProposalUndone {
		t.Fatalf("undo %#v %v", undone, e)
	}
}
func TestCrashReconcile(t *testing.T) {
	ctx := context.Background()
	repo := memproposal.New()
	mem := memmemory.New()
	p, c := fixture()
	rs, _ := repo.StageBatch(ctx, p, strings.Repeat("c", 64), []learning.Candidate{c}, nil)
	claimed, _ := repo.ClaimPromotion(ctx, p, rs[0].ID, rs[0].Version)
	wctx := tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{Source: tool.MemorySource{ProposalID: string(rs[0].ID)}})
	mem.RememberIfCurrent(wctx, tool.MemoryEntry{Key: c.Key, Value: c.Value, Description: c.Description}, tool.MemoryCurrent{})
	done, e := (memorypromotion.Promoter{Proposals: repo, Memory: mem}).Process(ctx, p, rs[0].ID, claimed.Version, memorypromotion.PolicyInput{})
	if e != nil || done.Status != learning.ProposalPromoted {
		t.Fatalf("reconcile %#v %v", done, e)
	}
	mr, _, _ := mem.Inspect(ctx, c.Key)
	if len(mr.Revisions) != 1 {
		t.Fatal("duplicate write")
	}
}
func TestPolicyReviewProcedureConflict(t *testing.T) {
	_, c := fixture()
	policy := memorypromotion.StandardPolicy{}
	if policy.Evaluate(memorypromotion.PolicyInput{Mode: learning.Review, Candidate: c}).Disposition != memorypromotion.DispositionReview {
		t.Fatal("review")
	}
	current := tool.MemoryRevision{Status: tool.MemoryStatusActive, Value: "other", Writer: tool.MemoryWriterUser}
	if policy.Evaluate(memorypromotion.PolicyInput{Mode: learning.Auto, TrustedProject: true, Candidate: c, Current: &current}).Disposition != memorypromotion.DispositionConflict {
		t.Fatal("explicit conflict")
	}
	c.Kind = learning.CandidateProcedure
	c.Key = ""
	c.Value = ""
	c.Description = ""
	c.Title = "title"
	c.Body = "body"
	if policy.Evaluate(memorypromotion.PolicyInput{Candidate: c}).Disposition != memorypromotion.DispositionUnsupported {
		t.Fatal("procedure")
	}
}

type racingMemory struct {
	*memmemory.Store
	beforeCAS func(context.Context)
}

func (m *racingMemory) RememberIfCurrent(ctx context.Context, entry tool.MemoryEntry, expected tool.MemoryCurrent) (tool.MemoryRecord, error) {
	if m.beforeCAS != nil {
		fn := m.beforeCAS
		m.beforeCAS = nil
		fn(ctx)
	}
	return m.Store.RememberIfCurrent(ctx, entry, expected)
}

func stage(t *testing.T, repo *memproposal.Store, digest string, candidate learning.Candidate) (learning.ProposalPartition, learning.ProposalRecord) {
	t.Helper()
	part, _ := fixture()
	records, err := repo.StageBatch(context.Background(), part, strings.Repeat(digest, 64), []learning.Candidate{candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return part, records[0]
}

func TestProcessDeduplicatesWithoutWriting(t *testing.T) {
	ctx := context.Background()
	repo := memproposal.New()
	memory := memmemory.New()
	_, candidate := fixture()
	if err := memory.RememberEntry(ctx, tool.MemoryEntry{Key: candidate.Key, Value: candidate.Value, Description: candidate.Description}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := memory.Inspect(ctx, candidate.Key)
	part, proposal := stage(t, repo, "d", candidate)

	done, err := (memorypromotion.Promoter{Proposals: repo, Memory: memory}).Process(ctx, part, proposal.ID, proposal.Version, memorypromotion.PolicyInput{Mode: learning.Auto, TrustedProject: true})
	if err != nil || done.Status != learning.ProposalPromoted || done.Receipt == nil {
		t.Fatalf("deduplicate: %#v %v", done, err)
	}
	after, _, _ := memory.Inspect(ctx, candidate.Key)
	if len(after.Revisions) != len(before.Revisions) || after.Current.Version != before.Current.Version {
		t.Fatalf("duplicate wrote a revision: before=%#v after=%#v", before, after)
	}
}

func TestProcessKeepsReviewAndConflictOutOfMemory(t *testing.T) {
	ctx := context.Background()
	t.Run("ambiguous duplicate remains staged", func(t *testing.T) {
		repo := memproposal.New()
		memory := memmemory.New()
		_, candidate := fixture()
		if err := memory.RememberEntry(ctx, tool.MemoryEntry{Key: "project/other", Value: candidate.Value}); err != nil {
			t.Fatal(err)
		}
		part, proposal := stage(t, repo, "e", candidate)
		done, err := (memorypromotion.Promoter{Proposals: repo, Memory: memory}).Process(ctx, part, proposal.ID, proposal.Version, memorypromotion.PolicyInput{Mode: learning.Auto, TrustedProject: true})
		if err != nil || done.Status != learning.ProposalStaged || len(done.Decisions) != 1 {
			t.Fatalf("review: %#v %v", done, err)
		}
		if _, found, _ := memory.Inspect(ctx, candidate.Key); found {
			t.Fatal("ambiguous duplicate was written")
		}
	})
	t.Run("explicit newer value conflicts", func(t *testing.T) {
		repo := memproposal.New()
		memory := memmemory.New()
		_, candidate := fixture()
		explicit := tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{Writer: tool.MemoryWriterUser, Origin: tool.MemoryOriginExplicit})
		if err := memory.RememberEntry(explicit, tool.MemoryEntry{Key: candidate.Key, Value: "newer explicit value"}); err != nil {
			t.Fatal(err)
		}
		part, proposal := stage(t, repo, "f", candidate)
		done, err := (memorypromotion.Promoter{Proposals: repo, Memory: memory}).Process(ctx, part, proposal.ID, proposal.Version, memorypromotion.PolicyInput{Mode: learning.Auto, TrustedProject: true})
		if err != nil || done.Status != learning.ProposalConflicted {
			t.Fatalf("conflict: %#v %v", done, err)
		}
		current, _, _ := memory.Inspect(ctx, candidate.Key)
		if current.Current.Value != "newer explicit value" {
			t.Fatal("explicit value was overwritten")
		}
	})
}

func TestProcessDefersProcedureAndLosesMemoryCASRaceSafely(t *testing.T) {
	ctx := context.Background()
	t.Run("procedure", func(t *testing.T) {
		repo := memproposal.New()
		memory := memmemory.New()
		_, candidate := fixture()
		candidate.Kind, candidate.Key, candidate.Value, candidate.Description = learning.CandidateProcedure, "", "", ""
		candidate.Title, candidate.Body = "Run tests", "Use the repository test task."
		part, proposal := stage(t, repo, "1", candidate)
		done, err := (memorypromotion.Promoter{Proposals: repo, Memory: memory}).Process(ctx, part, proposal.ID, proposal.Version, memorypromotion.PolicyInput{Mode: learning.Auto, TrustedProject: true})
		if err != nil || done.Status != learning.ProposalDeferredUnsupported {
			t.Fatalf("procedure: %#v %v", done, err)
		}
	})
	t.Run("memory CAS race", func(t *testing.T) {
		repo := memproposal.New()
		memory := &racingMemory{Store: memmemory.New()}
		_, candidate := fixture()
		part, proposal := stage(t, repo, "2", candidate)
		memory.beforeCAS = func(ctx context.Context) {
			explicit := tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{Writer: tool.MemoryWriterUser, Origin: tool.MemoryOriginExplicit})
			if err := memory.RememberEntry(explicit, tool.MemoryEntry{Key: candidate.Key, Value: "concurrent explicit value"}); err != nil {
				t.Error(err)
			}
		}
		done, err := (memorypromotion.Promoter{Proposals: repo, Memory: memory}).Process(ctx, part, proposal.ID, proposal.Version, memorypromotion.PolicyInput{Mode: learning.Auto, TrustedProject: true})
		if err != nil || done.Status != learning.ProposalConflicted {
			t.Fatalf("race: %#v %v", done, err)
		}
		current, _, _ := memory.Inspect(ctx, candidate.Key)
		if current.Current.Value != "concurrent explicit value" {
			t.Fatal("CAS race overwrote concurrent value")
		}
	})
}

type orderingRepository struct {
	learning.ProposalRepository
	operations *[]string
}

func (r orderingRepository) ClaimPromotion(ctx context.Context, part learning.ProposalPartition, id learning.ProposalID, version learning.ProposalVersion) (learning.ProposalRecord, error) {
	*r.operations = append(*r.operations, "claim")
	return r.ProposalRepository.ClaimPromotion(ctx, part, id, version)
}

func (r orderingRepository) Finalize(ctx context.Context, part learning.ProposalPartition, id learning.ProposalID, version learning.ProposalVersion, status learning.ProposalStatus, receipt *learning.PromotionReceipt, decision learning.Decision) (learning.ProposalRecord, error) {
	*r.operations = append(*r.operations, "finalize")
	return r.ProposalRepository.Finalize(ctx, part, id, version, status, receipt, decision)
}

type orderingMemory struct {
	*memmemory.Store
	operations *[]string
}

func (m orderingMemory) RememberIfCurrent(ctx context.Context, entry tool.MemoryEntry, expected tool.MemoryCurrent) (tool.MemoryRecord, error) {
	*m.operations = append(*m.operations, "remember")
	return m.Store.RememberIfCurrent(ctx, entry, expected)
}

func (m orderingMemory) UndoLatest(ctx context.Context, key string, expected tool.MemoryVersion) (tool.MemoryRecord, error) {
	*m.operations = append(*m.operations, "undo-write")
	return m.Store.UndoLatest(ctx, key, expected)
}

func TestPromotionAndUndoPersistInCrashSafeOrder(t *testing.T) {
	ctx := context.Background()
	baseRepo := memproposal.New()
	baseMemory := memmemory.New()
	_, candidate := fixture()
	part, proposal := stage(t, baseRepo, "4", candidate)
	operations := []string{}
	promoter := memorypromotion.Promoter{
		Proposals: orderingRepository{ProposalRepository: baseRepo, operations: &operations},
		Memory:    orderingMemory{Store: baseMemory, operations: &operations},
	}
	promoted, err := promoter.Process(ctx, part, proposal.ID, proposal.Version, memorypromotion.PolicyInput{Mode: learning.Auto, TrustedProject: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(operations, ","), "claim,remember,finalize"; got != want {
		t.Fatalf("promotion persistence order=%q, want %q", got, want)
	}
	operations = operations[:0]
	if _, err = promoter.Undo(ctx, part, proposal.ID, promoted.Version); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(operations, ","), "undo-write,finalize"; got != want {
		t.Fatalf("undo persistence order=%q, want %q", got, want)
	}
}

func TestUndoConflictsWithNewerRevision(t *testing.T) {
	ctx := context.Background()
	repo := memproposal.New()
	memory := memmemory.New()
	_, candidate := fixture()
	part, proposal := stage(t, repo, "3", candidate)
	promoter := memorypromotion.Promoter{Proposals: repo, Memory: memory}
	promoted, err := promoter.Process(ctx, part, proposal.ID, proposal.Version, memorypromotion.PolicyInput{Mode: learning.Auto, TrustedProject: true})
	if err != nil {
		t.Fatal(err)
	}
	explicit := tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{Writer: tool.MemoryWriterUser, Origin: tool.MemoryOriginExplicit})
	if err = memory.RememberEntry(explicit, tool.MemoryEntry{Key: candidate.Key, Value: "newer value"}); err != nil {
		t.Fatal(err)
	}
	undone, err := promoter.Undo(ctx, part, proposal.ID, promoted.Version)
	if err != nil || undone.Status != learning.ProposalConflicted {
		t.Fatalf("undo conflict: %#v %v", undone, err)
	}
	current, _, _ := memory.Inspect(ctx, candidate.Key)
	if current.Current.Value != "newer value" {
		t.Fatal("undo rolled back a newer revision")
	}
}
