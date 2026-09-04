package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func stagedProposalFixture(t *testing.T, repo learning.ProposalRepository) (learning.ProposalPartition, learning.ProposalRecord) {
	t.Helper()
	trajectory := learning.NewTrajectory("source", "", session.StopEndTurn, session.Usage{}, []session.Message{session.NewUserMessage("Remember concise output")})
	input := learning.NewInput(trajectory, nil, nil, nil)
	ref, err := learning.MessageEvidenceRef(input, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := learning.NewCandidate(input, learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "user/output", Value: "concise", Description: "output preference", Evidence: []learning.EvidenceRef{ref}})
	if err != nil {
		t.Fatal(err)
	}
	part := learning.ProposalPartition{Principal: "principal"}
	records, err := repo.StageBatch(context.Background(), part, "0123456789abcdef", []learning.Candidate{candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return part, records[0]
}

func TestLearningProposalServicePaginationDecisionCASAndUnavailable(t *testing.T) {
	repo := memproposal.New()
	_, record := stagedProposalFixture(t, repo)
	svc := &Service{cfg: Config{Store: memstore.New(), Proposals: repo, ProposalPrincipal: func(*session.Principal) string { return "principal" }}}
	page, err := svc.ListLearningProposals(context.Background(), "", "", 1, "")
	if err != nil || len(page.GetProposals()) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	updated, err := svc.DecideLearningProposal(context.Background(), string(record.ID), string(record.Version), "reject", "operator rejected", "")
	if err != nil || updated.GetStatus() != string(learning.ProposalRejected) {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	_, err = svc.DecideLearningProposal(context.Background(), string(record.ID), string(record.Version), "reject", "stale", "")
	if !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("stale err=%v", err)
	}
	if _, err := (&Service{}).ListLearningProposals(context.Background(), "", "", 1, ""); !errors.Is(err, ErrLearningUnavailable) {
		t.Fatalf("unavailable err=%v", err)
	}
}

func TestLearningTransportMappings(t *testing.T) {
	disabled := &Service{}
	h := &HarnessServer{svc: disabled}
	_, err := h.ListLearningProposals(context.Background(), &mecatlv1.ListLearningProposalsRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("gRPC disabled code=%v err=%v", status.Code(err), err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/learning/proposals", nil)
	res := httptest.NewRecorder()
	NewHTTPHandler(disabled).ServeHTTP(res, req)
	if res.Code != http.StatusNotImplemented {
		t.Fatalf("HTTP disabled status=%d body=%s", res.Code, res.Body.String())
	}

	repo := memproposal.New()
	_, record := stagedProposalFixture(t, repo)
	svc := &Service{cfg: Config{Store: memstore.New(), Proposals: repo, ProposalPrincipal: func(*session.Principal) string { return "principal" }}}
	transport := &HarnessServer{svc: svc}
	_, err = transport.DecideLearningProposal(context.Background(), &mecatlv1.DecideLearningProposalRequest{Id: string(record.ID), ExpectedVersion: "stale", Decision: "reject"})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("gRPC CAS code=%v err=%v", status.Code(err), err)
	}

	res = httptest.NewRecorder()
	NewHTTPHandler(svc).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/learning/proposals?limit=1", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("HTTP list status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestLearningHTTPJSONIsStrictAndBounded(t *testing.T) {
	repo := memproposal.New()
	_, record := stagedProposalFixture(t, repo)
	svc := &Service{cfg: Config{Store: memstore.New(), Proposals: repo, ProposalPrincipal: func(*session.Principal) string { return "principal" }}}
	handler := NewHTTPHandler(svc)
	path := "/v1/learning/proposals/" + string(record.ID) + "/decision"
	for name, body := range map[string]string{
		"duplicate top":    `{"expected_version":"` + string(record.Version) + `","decision":"reject","decision":"approve"}`,
		"duplicate nested": `{"expected_version":"` + string(record.Version) + `","decision":"reject","reason":{"value":"a","value":"b"}}`,
		"unknown":          `{"expected_version":"` + string(record.Version) + `","decision":"reject","extra":true}`,
		"trailing":         `{"expected_version":"` + string(record.Version) + `","decision":"reject"}{}`,
		"missing version":  `{"decision":"reject"}`,
		"oversized":        `{"expected_version":"` + string(record.Version) + `","decision":"reject","reason":"` + strings.Repeat("x", 5<<10) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
		})
	}
}

func TestLearningHTTPRejectsDuplicateKeysRecursivelyOnEveryMutation(t *testing.T) {
	for name, raw := range map[string]string{
		"top-level": `{"decision":"reject","decision":"approve"}`,
		"nested":    `{"outer":{"value":1,"value":2}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := rejectDuplicateJSONKeys([]byte(raw)); err == nil {
				t.Fatal("duplicate JSON key accepted")
			}
		})
	}

	svc := &Service{cfg: Config{Store: memstore.New(), Proposals: memproposal.New(), ReflectSession: func(context.Context, *session.Session) (ReflectionReceipt, error) {
		return ReflectionReceipt{}, nil
	}}}
	handler := NewHTTPHandler(svc)
	for name, request := range map[string]*http.Request{
		"reflect":  httptest.NewRequest(http.MethodPost, "/v1/sessions/s/reflect", strings.NewReader(`{"x":{"a":1,"a":2}}`)),
		"decision": httptest.NewRequest(http.MethodPost, "/v1/learning/proposals/p/decision", strings.NewReader(`{"decision":"reject","decision":"approve"}`)),
		"undo":     httptest.NewRequest(http.MethodPost, "/v1/learning/proposals/p/undo", strings.NewReader(`{"expected_version":"v1","expected_version":"v2"}`)),
	} {
		t.Run(name, func(t *testing.T) {
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, request)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
		})
	}
}

func TestScalableReflectionEvidence_Scenario9_TransportDispositionAndTypedErrorMatrix(t *testing.T) {
	completedStore := func(t *testing.T) *memstore.Store {
		t.Helper()
		store := memstore.New()
		sess := session.New("reflect-source", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
		if err := sess.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		if err := sess.Complete(); err != nil {
			t.Fatal(err)
		}
		if err := store.Save(context.Background(), sess); err != nil {
			t.Fatal(err)
		}
		return store
	}

	t.Run("closed abstention", func(t *testing.T) {
		svc := &Service{cfg: Config{Store: completedStore(t), ReflectSession: func(context.Context, *session.Session) (ReflectionReceipt, error) {
			return ReflectionReceipt{Abstained: true, Reason: "no_eligible_evidence"}, nil
		}}}
		grpcResp, err := (&HarnessServer{svc: svc}).ReflectSession(context.Background(), &mecatlv1.ReflectSessionRequest{SessionId: "reflect-source"})
		if err != nil {
			t.Fatal(err)
		}
		receipt := grpcResp.GetReceipt()
		if !receipt.GetAbstained() || receipt.GetDisposition() != "abstained" || receipt.GetReason() != "no_eligible_evidence" || receipt.GetMessage() != "No eligible evidence was available for reflection." {
			t.Fatalf("gRPC receipt = %+v", receipt)
		}
		res := httptest.NewRecorder()
		NewHTTPHandler(svc).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/sessions/reflect-source/reflect", strings.NewReader("{}")))
		if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "source transcript") || !strings.Contains(res.Body.String(), `"reason":"no_eligible_evidence"`) || !strings.Contains(res.Body.String(), "No eligible evidence was available for reflection.") {
			t.Fatalf("HTTP status=%d body=%s", res.Code, res.Body.String())
		}
	})

	for _, tc := range []struct {
		name       string
		err        error
		grpcCode   codes.Code
		httpStatus int
	}{
		{name: "cancelled", err: context.Canceled, grpcCode: codes.Canceled, httpStatus: 499},
		{name: "closed", err: ErrUnavailable, grpcCode: codes.Unavailable, httpStatus: http.StatusServiceUnavailable},
		{name: "source mismatch", err: ErrFailedPrecondition, grpcCode: codes.FailedPrecondition, httpStatus: http.StatusPreconditionFailed},
		{name: "queue full", err: ErrReflectionQueueFull, grpcCode: codes.ResourceExhausted, httpStatus: http.StatusTooManyRequests},
		{name: "timeout", err: context.DeadlineExceeded, grpcCode: codes.DeadlineExceeded, httpStatus: http.StatusGatewayTimeout},
		{name: "validation", err: ErrInvalidArgument, grpcCode: codes.InvalidArgument, httpStatus: http.StatusBadRequest},
		{name: "persistence", err: ErrReflectionFailed, grpcCode: codes.Unavailable, httpStatus: http.StatusServiceUnavailable},
		{name: "provider", err: fmt.Errorf("provider secret: %w", ErrReflectionFailed), grpcCode: codes.Unavailable, httpStatus: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{cfg: Config{Store: completedStore(t), ReflectSession: func(context.Context, *session.Session) (ReflectionReceipt, error) {
				return ReflectionReceipt{}, tc.err
			}}}
			_, err := (&HarnessServer{svc: svc}).ReflectSession(context.Background(), &mecatlv1.ReflectSessionRequest{SessionId: "reflect-source"})
			if status.Code(err) != tc.grpcCode || strings.Contains(err.Error(), "provider secret") {
				t.Fatalf("gRPC err=%v code=%v", err, status.Code(err))
			}
			res := httptest.NewRecorder()
			NewHTTPHandler(svc).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/sessions/reflect-source/reflect", strings.NewReader("{}")))
			if res.Code != tc.httpStatus || strings.Contains(res.Body.String(), "provider secret") || strings.Contains(res.Body.String(), `"code":"internal"`) {
				t.Fatalf("HTTP status=%d body=%s", res.Code, res.Body.String())
			}
		})
	}
}

func TestLearningProjectPartitionReviewRemainsReachableWithoutPromotionTrust(t *testing.T) {
	promoteCalls := 0
	svc := &Service{cfg: Config{Proposals: memproposal.New(), ProposalPrincipal: func(*session.Principal) string { return "principal" }, ProjectPromotionAllowed: func(string) bool { return false }, PromoteProposal: func(context.Context, learning.ProposalPartition, learning.ProposalID, learning.ProposalVersion, bool) (learning.ProposalRecord, error) {
		promoteCalls++
		return learning.ProposalRecord{}, nil
	}}}
	page, err := svc.ListLearningProposals(context.Background(), "", "", 1, "/project")
	if err != nil || len(page.GetProposals()) != 0 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if _, err = svc.DecideLearningProposal(context.Background(), "proposal", "version", "approve", "", "/project"); !errors.Is(err, ErrFailedPrecondition) || promoteCalls != 0 {
		t.Fatalf("untrusted project approval err=%v calls=%d", err, promoteCalls)
	}
}

func TestLearningUnavailableConvergenceTargetIsFailedPreconditionAndProjected(t *testing.T) {
	repo := memproposal.New()
	_, record := stagedProposalFixture(t, repo)
	svc := &Service{cfg: Config{
		Store: memstore.New(), Proposals: repo,
		ProposalPrincipal: func(*session.Principal) string { return "principal" },
		ProposalActionAvailable: func(string) (bool, string) {
			return false, "convergence-capable project memory target is unavailable"
		},
		PromoteProposal: func(context.Context, learning.ProposalPartition, learning.ProposalID, learning.ProposalVersion, bool) (learning.ProposalRecord, error) {
			t.Fatal("unavailable target reached promoter")
			return learning.ProposalRecord{}, nil
		},
	}}
	page, err := svc.ListLearningProposals(context.Background(), "", "", 10, "")
	if err != nil || len(page.GetProposals()) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	proposal := page.GetProposals()[0]
	if proposal.GetPromotionAvailable() || !strings.Contains(proposal.GetPromotionUnavailableReason(), "unavailable") {
		t.Fatalf("availability projection=%+v", proposal)
	}
	_, err = (&HarnessServer{svc: svc}).DecideLearningProposal(context.Background(), &mecatlv1.DecideLearningProposalRequest{Id: string(record.ID), ExpectedVersion: string(record.Version), Decision: "approve"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("gRPC code=%v err=%v", status.Code(err), err)
	}
}
