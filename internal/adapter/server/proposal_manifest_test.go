package server

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

type manifestTestRepository interface {
	learning.ProposalRepository
	StageBatchManifest(context.Context, learning.ProposalPartition, learning.MaterializationManifest, []learning.Candidate, []learning.Signal) ([]learning.ProposalRecord, error)
	GetManifest(context.Context, learning.ProposalPartition, learning.ProposalID) (learning.MaterializationManifest, bool, error)
}

type proposalManifestFixture struct {
	repo     manifestTestRepository
	store    *memstore.Store
	eventLog *memstore.EventLog
	part     learning.ProposalPartition
	record   learning.ProposalRecord
	manifest learning.MaterializationManifest
	loads    int
}

func newProposalManifestFixture(t *testing.T) *proposalManifestFixture {
	t.Helper()
	base := memproposal.New()
	repo, ok := any(base).(manifestTestRepository)
	if !ok {
		t.Fatal("memproposal does not persist materialization manifests")
	}
	messages := []session.Message{
		session.NewUserMessage("Remember concise output."),
		session.NewAssistantMessage("I will inspect it.", "private reasoning", []session.ToolCall{session.NewToolCall("call-1", "Read", []byte(`{"token":"ghp_0123456789abcdefghijklmnop"}`))}),
		session.NewToolMessage(session.NewToolResult("call-1", strings.Repeat("safe result ", 180))),
	}
	trajectory := learning.NewTrajectory("manifest-source", "", session.StopEndTurn, session.Usage{}, messages)
	events := []session.Event{{Type: session.EvResult, Seq: 7, Turn: 1, Result: &session.ResultPayload{Stop: session.StopEndTurn}}}
	materialized, err := learning.MaterializeEvidence(context.Background(), learning.MaterializationRequest{Trajectory: trajectory, Events: events, Explicit: true})
	if err != nil || materialized.Disposition != learning.MaterializationSelected {
		t.Fatalf("materialize = %+v, err=%v", materialized, err)
	}
	ref, err := learning.MessageEvidenceRef(materialized.Input, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	eventRef, err := learning.EventEvidenceRef(materialized.Input, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := learning.NewCandidate(materialized.Input, learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "user/output", Value: "concise", Evidence: []learning.EvidenceRef{ref, eventRef}})
	if err != nil {
		t.Fatal(err)
	}
	part := learning.ProposalPartition{Principal: "principal"}
	records, err := repo.StageBatchManifest(context.Background(), part, materialized.Manifest, []learning.Candidate{candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	sess := session.New(trajectory.SessionID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.SeedHistory(messages); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	eventLog := memstore.NewEventLog()
	if err := eventLog.Append(context.Background(), trajectory.SessionID, events[0]); err != nil {
		t.Fatal(err)
	}
	return &proposalManifestFixture{repo: repo, store: store, eventLog: eventLog, part: part, record: records[0], manifest: materialized.Manifest}
}

func (f *proposalManifestFixture) service() *Service {
	return &Service{cfg: Config{
		Store: f.store, EventLog: f.eventLog, Proposals: f.repo,
		ProposalPrincipal: func(*session.Principal) string { return "principal" },
		ProposalManifest: func(ctx context.Context, part learning.ProposalPartition, id learning.ProposalID) (learning.MaterializationManifest, bool, error) {
			f.loads++
			return f.repo.GetManifest(ctx, part, id)
		},
		PromoteProposal: func(ctx context.Context, part learning.ProposalPartition, id learning.ProposalID, version learning.ProposalVersion, _ bool) (learning.ProposalRecord, error) {
			claimed, err := f.repo.ClaimPromotion(ctx, part, id, version)
			if err != nil {
				return learning.ProposalRecord{}, err
			}
			return f.repo.Finalize(ctx, part, id, claimed.Version, learning.ProposalPromoted, &learning.PromotionReceipt{MemoryKey: "user/output", ResultVersion: "v1"}, learning.Decision{Kind: learning.DecisionApprove})
		},
		UndoProposal: func(ctx context.Context, part learning.ProposalPartition, id learning.ProposalID, version learning.ProposalVersion) (learning.ProposalRecord, error) {
			return f.repo.Finalize(ctx, part, id, version, learning.ProposalUndone, &learning.PromotionReceipt{MemoryKey: "user/output", ResultVersion: "v2"}, learning.Decision{Kind: learning.DecisionApprove})
		},
	}}
}

func TestADR_0300_ProposalVerificationBuildsSelectedEvidenceOnly(t *testing.T) {
	original := 0
	messages := []session.Message{
		session.NewUserMessage("remember selected evidence"),
		session.NewUserMessageWithParts("", []session.Content{{Data: make([]byte, 16<<20)}}),
	}
	entries := []learning.ManifestEntry{{Locator: learning.EvidenceMessage, OriginalMessage: &original}}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	selected, eventSequences, err := selectManifestSource(messages, entries)
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Text != "remember selected evidence" || len(eventSequences) != 0 {
		t.Fatalf("selected source = %#v, events=%v", selected, eventSequences)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= 1<<20 {
		t.Fatalf("selected-only source traversal allocated %d bytes for 16 MiB of unrelated retained content", allocated)
	}
}

func TestScalableReflectionEvidence_Scenario4_ProposalPersistsCompleteAggregateManifest(t *testing.T) {
	f := newProposalManifestFixture(t)
	got, found, err := f.repo.GetManifest(context.Background(), f.part, f.record.ID)
	if err != nil || !found {
		t.Fatalf("manifest found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(got, f.manifest) {
		t.Fatalf("persisted manifest = %#v, want %#v", got, f.manifest)
	}
	got.Entries[0].Digest = strings.Repeat("0", 64)
	again, _, _ := f.repo.GetManifest(context.Background(), f.part, f.record.ID)
	if again.Entries[0].Digest != f.manifest.Entries[0].Digest {
		t.Fatal("returned manifest aliases persisted state")
	}
	tampered := f.manifest
	tampered.Entries = append([]learning.ManifestEntry(nil), f.manifest.Entries...)
	tampered.Entries[1].Component = "tool:substituted"
	if _, err := f.repo.StageBatchManifest(context.Background(), f.part, tampered, []learning.Candidate{f.record.Candidate}, nil); !errors.Is(err, learning.ErrInvalidProposal) {
		t.Fatalf("idempotent stage accepted a substituted manifest entry: %v", err)
	}
}

func TestScalableReflectionEvidence_Scenario4_ListIsMetadataOnlyDetailRematerializesOnce(t *testing.T) {
	f := newProposalManifestFixture(t)
	svc := f.service()
	page, err := svc.ListLearningProposals(context.Background(), "", "", 10, "")
	if err != nil || len(page.GetProposals()) != 1 || f.loads != 0 {
		t.Fatalf("list proposals=%d loads=%d err=%v", len(page.GetProposals()), f.loads, err)
	}
	if got := page.GetProposals()[0].GetEvidence()[0].GetPreview(); got != "" {
		t.Fatalf("metadata-only list exposed preview %q", got)
	}
	detail, err := svc.GetLearningProposal(context.Background(), string(f.record.ID), "")
	if err != nil || f.loads != 1 || !detail.GetEvidence()[0].GetAvailable() {
		t.Fatalf("detail loads=%d available=%v err=%v", f.loads, detail.GetEvidence()[0].GetAvailable(), err)
	}
}

func TestScalableReflectionEvidence_Scenario4_ApprovalRevalidatesManifestOnce(t *testing.T) {
	f := newProposalManifestFixture(t)
	got, err := f.service().DecideLearningProposal(context.Background(), string(f.record.ID), string(f.record.Version), "approve", "", "")
	if err != nil || f.loads != 1 || got.GetStatus() != string(learning.ProposalPromoted) {
		t.Fatalf("approval status=%q loads=%d err=%v", got.GetStatus(), f.loads, err)
	}
}

type tamperedCitationRepository struct{ manifestTestRepository }

func (r tamperedCitationRepository) Get(ctx context.Context, part learning.ProposalPartition, id learning.ProposalID) (learning.ProposalRecord, bool, error) {
	record, found, err := r.manifestTestRepository.Get(ctx, part, id)
	if found && len(record.Candidate.Evidence) > 0 {
		record.Candidate.Evidence[0].ManifestIndex++
	}
	return record, found, err
}

func TestScalableReflectionEvidence_Scenario4_MismatchFailsPreconditionWithoutPromotion(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*learning.MaterializationManifest)
		citation bool
		source   bool
	}{
		{name: "protocol", mutate: func(m *learning.MaterializationManifest) { m.Protocol = "future" }},
		{name: "source boundary", mutate: func(m *learning.MaterializationManifest) { m.Source.Domain = "wrong" }},
		{name: "message coordinate", mutate: func(m *learning.MaterializationManifest) { *m.Entries[0].OriginalMessage = 99 }},
		{name: "event sequence", mutate: func(m *learning.MaterializationManifest) { *m.Entries[len(m.Entries)-1].EventSequence = 99 }},
		{name: "entry digest", mutate: func(m *learning.MaterializationManifest) { m.Entries[0].Digest = strings.Repeat("0", 64) }},
		{name: "tool component", mutate: func(m *learning.MaterializationManifest) { m.Entries[1].Component = "tool:wrong" }},
		{name: "aggregate digest", mutate: func(m *learning.MaterializationManifest) { m.Digest = strings.Repeat("0", 64) }},
		{name: "candidate citation", citation: true},
		{name: "changed source", source: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newProposalManifestFixture(t)
			svc := f.service()
			if tc.mutate != nil {
				base := svc.cfg.ProposalManifest
				svc.cfg.ProposalManifest = func(ctx context.Context, part learning.ProposalPartition, id learning.ProposalID) (learning.MaterializationManifest, bool, error) {
					manifest, found, err := base(ctx, part, id)
					tc.mutate(&manifest)
					return manifest, found, err
				}
			}
			if tc.citation {
				svc.cfg.Proposals = tamperedCitationRepository{f.repo}
			}
			if tc.source {
				sess, err := f.store.Load(context.Background(), "manifest-source")
				if err != nil {
					t.Fatal(err)
				}
				changed := append([]session.Message(nil), sess.Conversation.Messages...)
				changed[0] = session.NewUserMessage("changed source")
				fresh := session.New("manifest-source", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(2, 0))
				if err := fresh.SeedHistory(changed); err != nil {
					t.Fatal(err)
				}
				if err := f.store.Save(context.Background(), fresh); err != nil {
					t.Fatal(err)
				}
			}
			promotions := 0
			original := svc.cfg.PromoteProposal
			svc.cfg.PromoteProposal = func(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, v learning.ProposalVersion, approved bool) (learning.ProposalRecord, error) {
				promotions++
				return original(ctx, p, id, v, approved)
			}
			_, err := svc.DecideLearningProposal(context.Background(), string(f.record.ID), string(f.record.Version), "approve", "", "")
			if !errors.Is(err, ErrFailedPrecondition) || promotions != 0 || f.loads != 1 {
				t.Fatalf("mismatch err=%v promotions=%d loads=%d", err, promotions, f.loads)
			}
		})
	}
}

func TestADR_0300_EvidencePreviewCompatibilityRemainsRedactedAndBounded(t *testing.T) {
	f := newProposalManifestFixture(t)
	detail, err := f.service().GetLearningProposal(context.Background(), string(f.record.ID), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, evidence := range detail.GetEvidence() {
		if len(evidence.GetPreview()) > 1024 || strings.Contains(evidence.GetPreview(), "ghp_0123456789abcdefghijklmnop") || strings.Contains(evidence.GetPreview(), "private reasoning") || strings.Contains(evidence.GetPreview(), `"token"`) {
			t.Fatalf("unsafe preview (%d bytes): %q", len(evidence.GetPreview()), evidence.GetPreview())
		}
	}
}

func TestADR_0300_V1EventEvidencePreviewUsesSessionStreamOrdinal(t *testing.T) {
	const sourceID = session.SessionID("event-preview-source")
	events := []session.Event{
		{Type: session.EvToolResult, Seq: 1, RunID: "run-one", ToolResult: &session.ToolResult{CallID: "first", Content: "first run result"}},
		{Type: session.EvToolResult, Seq: 1, RunID: "run-two", ToolResult: &session.ToolResult{CallID: "second", Content: "second run result"}},
	}
	materialized, err := learning.MaterializeEvidence(context.Background(), learning.MaterializationRequest{
		Trajectory: learning.NewTrajectory(sourceID, "", session.StopEndTurn, session.Usage{}, nil),
		Events:     events,
		Explicit:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := learning.EventEvidenceRef(materialized.Input, 1, "")
	if err != nil {
		t.Fatal(err)
	}

	store := memstore.New()
	sess := session.New(sourceID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := store.Create(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	eventLog := memstore.NewEventLog()
	for _, event := range events {
		if err := eventLog.Append(context.Background(), sourceID, event); err != nil {
			t.Fatal(err)
		}
	}

	available, availability, preview := (&Service{cfg: Config{Store: store, EventLog: eventLog}}).learningEvidenceStatus(context.Background(), ref)
	if !available || availability != "available" || !strings.Contains(preview, "second run result") {
		t.Fatalf("v1 evidence status = available=%v availability=%q preview=%q", available, availability, preview)
	}
}

func TestADR_0300_StagingPromotionAndUndoControlsUnchanged(t *testing.T) {
	f := newProposalManifestFixture(t)
	svc := f.service()
	promoted, err := svc.DecideLearningProposal(context.Background(), string(f.record.ID), string(f.record.Version), "approve", "", "")
	if err != nil || promoted.GetStatus() != string(learning.ProposalPromoted) {
		t.Fatalf("promote=%+v err=%v", promoted, err)
	}
	undone, err := svc.UndoLearningPromotion(context.Background(), string(f.record.ID), promoted.GetVersion(), "")
	if err != nil || undone.GetStatus() != string(learning.ProposalUndone) {
		t.Fatalf("undo=%+v err=%v", undone, err)
	}
}
