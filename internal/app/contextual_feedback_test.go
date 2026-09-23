package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestPlanApprovalReceiptLifecycleClearsThroughRealService(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear func(context.Context, *server.Service, session.SessionID) error
	}{
		{"close", func(_ context.Context, svc *server.Service, id session.SessionID) error {
			svc.CloseSession(id)
			return nil
		}},
		{"clear successor", func(ctx context.Context, svc *server.Service, id session.SessionID) error {
			_, err := svc.ClearSessionSuccessor(ctx, id, server.SuccessorPlacement{})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newPlanApprovalReceipts()
			cfg := guardrailE2ECfg(t, true, PostureAuto, "printf lifecycle")
			cfg.planApprovals = store
			built, err := Build(context.Background(), cfg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()
			sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			store.RecordPlanApproval(agent.PlanApprovalReceipt{Ref: "r", SessionID: sess.ID, Call: "c", TargetMode: session.ModeDefault})
			if err := tc.clear(context.Background(), built.Service, sess.ID); err != nil {
				t.Fatalf("cleanup: %v", err)
			}
			if _, ok := store.ConsumePlanApproval(sess.ID); ok {
				t.Fatal("service lifecycle retained pending plan receipt")
			}
		})
	}
}

func TestPlanApprovalReceiptIsSingleUseModeBoundAndClearable(t *testing.T) {
	store := newPlanApprovalReceipts()
	receipt := agent.PlanApprovalReceipt{Ref: "r", SessionID: "s", Call: "c", TargetMode: session.ModeDefault}
	store.RecordPlanApproval(receipt)
	got, ok := store.ConsumePlanApproval("s")
	if !ok || got != receipt {
		t.Fatalf("consume = %+v, %v", got, ok)
	}
	if _, ok := store.ConsumePlanApproval("s"); ok {
		t.Fatal("plan receipt was consumed twice")
	}
	store.RecordPlanApproval(receipt)
	store.ClearPlanApprovals("s")
	if _, ok := store.ConsumePlanApproval("s"); ok {
		t.Fatal("cleared plan receipt remained available")
	}
}

func TestPlanApprovalReceiptRejectsNonPlanAndReleaseAuthority(t *testing.T) {
	store := newPlanApprovalReceipts()
	for _, receipt := range []agent.PlanApprovalReceipt{
		{SessionID: "s", Call: "c", TargetMode: session.ModeDefault},
		{Ref: "r", SessionID: "s", TargetMode: session.ModeDefault},
		{Ref: "r", SessionID: "s", Call: "c", TargetMode: session.ModePlan},
	} {
		store.RecordPlanApproval(receipt)
	}
	if _, ok := store.ConsumePlanApproval("s"); ok {
		t.Fatal("non-plan or denied receipt created execution authority")
	}
}

func TestGuardrailsTaskWindowClamp(t *testing.T) {
	for input, want := range map[int]int{-9: 1, 0: 1, 1: 1, 2: 2, 3: 3, 99: 3} {
		if got := clampReviewTaskWindow(input); got != want {
			t.Errorf("clampReviewTaskWindow(%d) = %d, want %d", input, got, want)
		}
	}
}
