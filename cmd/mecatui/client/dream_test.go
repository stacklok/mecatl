package client

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type fakeDreamClient struct {
	generateTarget string
	decideID       string
	decision       string
	plan           DreamPlan
	receipt        DreamReceipt
	err            error
}

func (f *fakeDreamClient) GenerateDreamPlan(_ context.Context, target string) (DreamPlan, error) {
	f.generateTarget = target
	return f.plan, f.err
}
func (f *fakeDreamClient) DecideDreamPlan(_ context.Context, id, decision string) (DreamReceipt, error) {
	f.decideID, f.decision = id, decision
	return f.receipt, f.err
}

type dreamHarness struct {
	mecatlv1.UnimplementedHarnessServiceServer
	generate *mecatlv1.GenerateDreamPlanRequest
	decide   *mecatlv1.DecideDreamPlanRequest
	err      error
}

func (s *dreamHarness) GenerateDreamPlan(_ context.Context, req *mecatlv1.GenerateDreamPlanRequest) (*mecatlv1.GenerateDreamPlanResponse, error) {
	s.generate = req
	if s.err != nil {
		return nil, s.err
	}
	return &mecatlv1.GenerateDreamPlanResponse{Plan: &mecatlv1.DreamReviewPlan{Id: "wire-plan", Target: req.GetTarget()}}, nil
}

func (s *dreamHarness) DecideDreamPlan(_ context.Context, req *mecatlv1.DecideDreamPlanRequest) (*mecatlv1.DecideDreamPlanResponse, error) {
	s.decide = req
	if s.err != nil {
		return nil, s.err
	}
	return &mecatlv1.DecideDreamPlanResponse{Receipt: &mecatlv1.DreamReceipt{Id: req.GetPlanId(), Disposition: req.GetDecision()}}, nil
}

func newDreamWireClient(t *testing.T, service *dreamHarness) *Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///dream", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); server.Stop(); _ = listener.Close() })
	return &Client{conn: conn, svc: mecatlv1.NewHarnessServiceClient(conn)}
}

func TestDreamGeneratedRPCPayloadsAndErrors(t *testing.T) {
	service := &dreamHarness{}
	client := newDreamWireClient(t, service)
	plan, err := client.GenerateDreamPlan(context.Background(), DreamTargetUserModel)
	if err != nil || plan.ID != "wire-plan" || service.generate.GetTarget() != DreamTargetUserModel {
		t.Fatalf("generate plan=%+v request=%+v err=%v", plan, service.generate, err)
	}
	receipt, err := client.DecideDreamPlan(context.Background(), "wire-plan", DreamDecisionDismiss)
	if err != nil || receipt.ID != "wire-plan" || service.decide.GetPlanId() != "wire-plan" || service.decide.GetDecision() != DreamDecisionDismiss {
		t.Fatalf("decide receipt=%+v request=%+v err=%v", receipt, service.decide, err)
	}
	service.err = status.Error(codes.Unavailable, "down")
	if _, err := client.GenerateDreamPlan(context.Background(), DreamTargetProjectMemory); status.Code(err) != codes.Unavailable {
		t.Fatalf("RPC error = %v", err)
	}
}

func TestDreamMappers(t *testing.T) {
	expires := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan := mapDreamPlan(&mecatlv1.DreamReviewPlan{Id: "p\xff", Target: DreamTargetProjectMemory, ExpiresAt: timestamppb.New(expires), PlannedOperationCount: 1, PlannedSourceCount: 2, Operations: []*mecatlv1.DreamOperation{{Kind: "synthesis", Survivor: &mecatlv1.DreamParticipant{Key: "keep", Value: "old", Description: "desc"}, Sources: []*mecatlv1.DreamParticipant{{Key: "drop"}}, Replacement: &mecatlv1.DreamReplacement{Value: "new", Description: "new desc"}, Reason: "because", ExactDuplicateEligible: true}}})
	if plan.ID != "p�" || !plan.ExpiresAt.Equal(expires) || plan.PlannedOperationCount != 1 || plan.SourceCount != 2 || len(plan.Operations) != 1 {
		t.Fatalf("plan mapping = %+v", plan)
	}
	op := plan.Operations[0]
	if op.Survivor.Key != "keep" || len(op.Sources) != 1 || op.Replacement.Value != "new" || !op.ExactDuplicateEligible {
		t.Fatalf("operation mapping = %+v", op)
	}
	r := mapDreamReceipt(&mecatlv1.DreamReceipt{Id: "p", Target: DreamTargetUserModel, Disposition: "partial", PlannedSourceCount: 5, AppliedSourceCount: 1, ConflictedSourceCount: 2, SkippedSourceCount: 1, FailedSourceCount: 1})
	if r.Planned != 5 || r.Applied != 1 || r.Conflicted != 2 || r.Skipped != 1 || r.Failed != 1 {
		t.Fatalf("receipt mapping = %+v", r)
	}
	if got := mapDreamPlan(nil); got.ID != "" || got.Operations != nil {
		t.Fatalf("nil plan mapper = %+v", got)
	}
	if got := mapDreamReceipt(nil); got != (DreamReceipt{}) {
		t.Fatalf("nil receipt mapper = %+v", got)
	}
}

func TestDreamCommandsPreserveExactPayloadAndErrors(t *testing.T) {
	f := &fakeDreamClient{plan: DreamPlan{ID: "p"}}
	msg := GenerateDreamPlanCmd(context.Background(), f, DreamTargetUserModel, 4, 8)().(DreamMsg)
	if f.generateTarget != DreamTargetUserModel || msg.Generation != 4 || msg.RequestID != 8 || msg.Plan.ID != "p" {
		t.Fatalf("generate message=%+v target=%q", msg, f.generateTarget)
	}
	f.receipt = DreamReceipt{ID: "p", Disposition: "dismissed"}
	msg = DecideDreamPlanCmd(context.Background(), f, "p", DreamDecisionDismiss, 5, 9)().(DreamMsg)
	if f.decideID != "p" || f.decision != DreamDecisionDismiss || msg.Receipt.Disposition != "dismissed" {
		t.Fatalf("decide message=%+v payload=%q/%q", msg, f.decideID, f.decision)
	}
	f.err = errors.New("rpc failed")
	msg = GenerateDreamPlanCmd(context.Background(), f, DreamTargetProjectMemory, 6, 10)().(DreamMsg)
	if !errors.Is(msg.Err, f.err) {
		t.Fatalf("error = %v", msg.Err)
	}
	if !IsDreamPlanGone(status.Error(codes.NotFound, "gone")) || IsDreamPlanGone(status.Error(codes.FailedPrecondition, "conflict")) {
		t.Fatal("plan-gone classification mismatch")
	}
	for _, tc := range []struct {
		code codes.Code
		want DreamDecisionErrorKind
	}{
		{codes.Aborted, DreamDecisionInProgress},
		{codes.FailedPrecondition, DreamDecisionConflict},
		{codes.AlreadyExists, DreamDecisionTerminalConflict},
		{codes.NotFound, DreamDecisionPlanGone},
		{codes.Unavailable, DreamDecisionUnknown},
	} {
		if got := ClassifyDreamDecisionError(status.Error(tc.code, "safe")); got != tc.want {
			t.Errorf("classify %v = %v, want %v", tc.code, got, tc.want)
		}
	}
}
