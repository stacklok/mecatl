package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLearningProposalOwnershipAndEvidenceResolution(t *testing.T) {
	alice := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser}
	trajectory := learning.NewTrajectory("source", "", session.StopEndTurn, session.Usage{}, []session.Message{session.NewUserMessage("Remember concise output")})
	evidenceEvent := session.Event{Type: session.EvToolCall, Seq: 7, Turn: 1, ToolCall: &session.ToolCall{ID: "call-1", Name: "Read"}}
	input := learning.NewInput(trajectory, []session.Event{evidenceEvent}, nil, nil)
	ref, err := learning.MessageEvidenceRef(input, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	eventRef, err := learning.EventEvidenceRef(input, 0, "call-1")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := learning.NewCandidate(input, learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "user/output", Value: "concise", Description: "output preference", Evidence: []learning.EvidenceRef{ref, eventRef}})
	if err != nil {
		t.Fatal(err)
	}
	repo := memproposal.New()
	part := learning.ProposalPartition{Principal: alice.Subject}
	records, err := repo.StageBatch(context.Background(), part, strings.Repeat("a", 64), []learning.Candidate{candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	source := session.New("source", session.ModeDefault, "", session.Limits{}, time.Unix(1, 0))
	source.Owner = alice
	if err := source.RecordUserPrompt("Remember concise output", nil); err != nil {
		t.Fatal(err)
	}
	if err := source.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := source.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	eventLog := memstore.NewEventLog()
	if err := eventLog.Append(context.Background(), source.ID, evidenceEvent); err != nil {
		t.Fatal(err)
	}
	promoteCalls := 0
	svc := &Service{cfg: Config{Store: store, EventLog: eventLog, OwnershipEnforced: true, Proposals: repo, PromoteProposal: func(context.Context, learning.ProposalPartition, learning.ProposalID, learning.ProposalVersion, bool) (learning.ProposalRecord, error) {
		promoteCalls++
		return learning.ProposalRecord{}, nil
	}, ProposalPrincipal: func(p *session.Principal) string {
		if p == nil {
			return ""
		}
		return p.Subject
	}}}
	aliceCtx := session.WithPrincipal(context.Background(), alice)
	got, err := svc.GetLearningProposal(aliceCtx, string(records[0].ID), "")
	if err != nil || len(got.GetEvidence()) != 2 || !got.GetEvidence()[0].GetAvailable() || !got.GetEvidence()[1].GetAvailable() {
		t.Fatalf("authorized evidence = %+v, err=%v", got, err)
	}
	if messagePreview, eventPreview := got.GetEvidence()[0].GetPreview(), got.GetEvidence()[1].GetPreview(); !strings.Contains(messagePreview, "Remember concise output") || !strings.Contains(eventPreview, `"type":"tool.call"`) || !strings.Contains(eventPreview, `"name":"Read"`) {
		t.Fatalf("canonical previews: message=%q event=%q", messagePreview, eventPreview)
	}
	for name, mutate := range map[string]func(*learning.EvidenceRef){
		"digest":    func(r *learning.EvidenceRef) { r.Digest = strings.Repeat("0", 64) },
		"sequence":  func(r *learning.EvidenceRef) { seq := int64(8); r.EventSeq = &seq },
		"tool call": func(r *learning.EvidenceRef) { r.ToolCallID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			ref := eventRef
			mutate(&ref)
			available, availability, preview := svc.learningEvidenceStatus(aliceCtx, ref)
			if available || availability != "evidence changed" || preview != "" {
				t.Fatalf("tampered event evidence = available %v, availability %q, preview %q", available, availability, preview)
			}
		})
	}
	bobCtx := session.WithPrincipal(context.Background(), bob)
	if available, availability, preview := svc.learningEvidenceStatus(bobCtx, ref); available || availability != "source unavailable" || preview != "" {
		t.Fatalf("cross-owner evidence = available %v, availability %q, preview %q", available, availability, preview)
	}
	page, err := svc.ListLearningProposals(bobCtx, "", "", 10, "")
	if err != nil || len(page.GetProposals()) != 0 {
		t.Fatalf("cross-owner page = %+v, err=%v", page, err)
	}
	if _, err := svc.GetLearningProposal(bobCtx, string(records[0].ID), ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner get err=%v", err)
	}
	grpcEndpoint := &HarnessServer{svc: svc}
	if _, err := grpcEndpoint.GetLearningProposal(aliceCtx, &mecatlv1.GetLearningProposalRequest{Id: string(records[0].ID)}); err != nil {
		t.Fatalf("owner gRPC get: %v", err)
	}
	if _, err := grpcEndpoint.GetLearningProposal(bobCtx, &mecatlv1.GetLearningProposalRequest{Id: string(records[0].ID)}); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-owner gRPC code=%v err=%v", status.Code(err), err)
	}
	httpEndpoint := NewHTTPHandler(svc)
	ownerHTTP := httptest.NewRecorder()
	httpEndpoint.ServeHTTP(ownerHTTP, httptest.NewRequest(http.MethodGet, "/v1/learning/proposals/"+string(records[0].ID), nil).WithContext(aliceCtx))
	if ownerHTTP.Code != http.StatusOK {
		t.Fatalf("owner HTTP status=%d body=%s", ownerHTTP.Code, ownerHTTP.Body.String())
	}
	otherHTTP := httptest.NewRecorder()
	httpEndpoint.ServeHTTP(otherHTTP, httptest.NewRequest(http.MethodGet, "/v1/learning/proposals/"+string(records[0].ID), nil).WithContext(bobCtx))
	if otherHTTP.Code != http.StatusNotFound {
		t.Fatalf("cross-owner HTTP status=%d body=%s", otherHTTP.Code, otherHTTP.Body.String())
	}
	if err := store.Delete(context.Background(), "source"); err != nil {
		t.Fatal(err)
	}
	got, err = svc.GetLearningProposal(aliceCtx, string(records[0].ID), "")
	if err != nil || got.GetEvidence()[0].GetAvailable() || got.GetEvidence()[1].GetAvailable() || got.GetEvidence()[0].GetAvailability() != "source unavailable" || got.GetEvidence()[1].GetAvailability() != "source unavailable" {
		t.Fatalf("deleted evidence = %+v, err=%v", got.GetEvidence(), err)
	}
	if _, err = svc.DecideLearningProposal(aliceCtx, string(records[0].ID), string(records[0].Version), "approve", "", ""); !errors.Is(err, ErrFailedPrecondition) || promoteCalls != 0 {
		t.Fatalf("unavailable evidence approval err=%v promoteCalls=%d", err, promoteCalls)
	}
}

func TestExplicitReflectionEnforcesSessionOwnershipAndWorksHeadless(t *testing.T) {
	alice := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser}
	completed := func(id session.SessionID, owner *session.Principal) *session.Session {
		sess := session.New(id, session.ModeDefault, "", session.Limits{}, time.Unix(1, 0))
		sess.Owner = owner
		if err := sess.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		if err := sess.Complete(); err != nil {
			t.Fatal(err)
		}
		return sess
	}
	store := memstore.New()
	if err := store.Save(context.Background(), completed("owned", alice)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	reflector := func(context.Context, *session.Session) (ReflectionReceipt, error) {
		calls++
		return ReflectionReceipt{Disposition: "completed", Abstained: true}, nil
	}
	svc := &Service{cfg: Config{Store: store, OwnershipEnforced: true, ReflectSession: reflector}}
	if _, err := svc.ReflectSession(session.WithPrincipal(context.Background(), bob), "owned"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner reflect err=%v", err)
	}
	if receipt, err := svc.ReflectSession(session.WithPrincipal(context.Background(), alice), "owned"); err != nil || !receipt.GetAbstained() {
		t.Fatalf("owner receipt=%+v err=%v", receipt, err)
	}
	headlessStore := memstore.New()
	if err := headlessStore.Save(context.Background(), completed("headless", nil)); err != nil {
		t.Fatal(err)
	}
	headless := &Service{cfg: Config{Store: headlessStore, ReflectSession: reflector}}
	if _, err := headless.ReflectSession(context.Background(), "headless"); err != nil {
		t.Fatalf("headless reflect: %v", err)
	}
	if calls != 2 {
		t.Fatalf("reflector calls=%d", calls)
	}
}

func TestLearningCapabilityTruth(t *testing.T) {
	if caps := (&Service{}).capabilities(); caps.GetReflection() || caps.GetLearningProposals() {
		t.Fatalf("disabled capabilities = %+v", caps)
	}
	svc := &Service{cfg: Config{
		Proposals: memproposal.New(), ProposalPrincipal: func(*session.Principal) string { return "principal" },
		ReflectSession: func(context.Context, *session.Session) (ReflectionReceipt, error) {
			return ReflectionReceipt{}, nil
		},
	}}
	if caps := svc.capabilities(); !caps.GetReflection() || !caps.GetLearningProposals() {
		t.Fatalf("enabled capabilities = %+v", caps)
	}
}

func TestLearningProposalPaginationCursor(t *testing.T) {
	repo := memproposal.New()
	part, first := stagedProposalFixture(t, repo)
	for _, suffix := range []string{"b", "c"} {
		candidate := first.Candidate
		candidate.Key += "/" + suffix
		candidate.Value += suffix
		if _, err := repo.StageBatch(context.Background(), part, strings.Repeat(suffix, 64), []learning.Candidate{candidate}, nil); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{cfg: Config{Store: memstore.New(), Proposals: repo, ProposalPrincipal: func(*session.Principal) string { return part.Principal }}}
	page1, err := svc.ListLearningProposals(context.Background(), "", "", 1, "")
	if err != nil || len(page1.GetProposals()) != 1 || page1.GetNextCursor() == "" {
		t.Fatalf("page1=%+v err=%v", page1, err)
	}
	page2, err := svc.ListLearningProposals(context.Background(), "", page1.GetNextCursor(), 1, "")
	if err != nil || len(page2.GetProposals()) != 1 || page2.GetProposals()[0].GetId() == page1.GetProposals()[0].GetId() {
		t.Fatalf("page2=%+v err=%v", page2, err)
	}
}

func TestLearningProjectionRepairsControlsAndWithholdsSecrets(t *testing.T) {
	record := learning.ProposalRecord{
		ID: "proposal-\xff\x1b", Version: "v\x00", Status: learning.ProposalStaged,
		Candidate: learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "api_token", Value: "sk-abcdefghijklmnopqrstuvwxyz123456", Description: "bad\x1b\xff"},
		Decisions: []learning.Decision{{Kind: learning.DecisionReject, Actor: "op\x00", Reason: "Bearer sk-abcdefghijklmnopqrstuvwxyz123456"}},
	}
	got := (&Service{}).toProtoLearningProposal(context.Background(), record)
	for name, value := range map[string]string{"id": got.GetId(), "version": got.GetVersion(), "description": got.GetDescription(), "actor": got.GetDecisions()[0].GetActor(), "reason": got.GetDecisions()[0].GetReason()} {
		if !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\x1b") {
			t.Fatalf("%s is not wire safe: %q", name, value)
		}
	}
	if !strings.Contains(got.GetValue(), "withheld") || !strings.Contains(got.GetDecisions()[0].GetReason(), "withheld") {
		t.Fatalf("secret projection = value %q reason %q", got.GetValue(), got.GetDecisions()[0].GetReason())
	}
}
