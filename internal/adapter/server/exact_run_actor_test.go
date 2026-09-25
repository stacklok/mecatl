package server_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type actorGuardrailHook struct{}

func (actorGuardrailHook) Run(_ context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	if ev.Phase == governance.PhasePreToolUse && ev.Tool == "Write" {
		return governance.HookOutcome{Block: true, AskApproval: true, Message: "review Write"}, nil
	}
	return governance.HookOutcome{}, nil
}

func exactRunActorService(t *testing.T, scoped bool, turns ...mockllm.Turn) (*server.Service, *memstore.EventLog) {
	t.Helper()
	store := memstore.New()
	log := memstore.NewEventLog()
	if len(turns) == 0 {
		turns = []mockllm.Turn{mockllm.ToolCallTurn(call("w1", "Write", `{"path":"a.go"}`)), mockllm.TextTurn("done")}
	}
	llm := mockllm.New(turns...)
	cat := tool.NewCatalog()
	var ran atomic.Int64
	cat.MustRegister(&writeAskTool{ran: &ran})
	policy := permpolicy.NewPolicy(nil, permstore.New())
	var hooks port.HookRunner
	if scoped {
		policy = permpolicy.NewPolicy(allowRules(), nil)
		hooks = actorGuardrailHook{}
	}
	eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: policy, Hooks: hooks, Model: "test-model", Interactive: true, Store: store})
	svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store, EventLog: log, Now: func() time.Time { return time.Unix(0, 0) }, DefaultCapabilities: llm.Capabilities()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, log
}

// TestADR_0204_ExactRunApprovalUsesVerdictCaller pins actor attribution for
// live ordinary and guardrail-scoped controls through the real gRPC relay.
// The direct case proves an absent verdict caller does not inherit the starter.
func TestADR_0204_ExactRunApprovalUsesVerdictCaller(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scoped bool
		origin session.ApprovalOrigin
	}{
		{name: "ordinary", origin: session.ApprovalOriginPermission},
		{name: "guardrail scoped", scoped: true, origin: session.ApprovalOriginHookGuardrail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, log := exactRunActorService(t, tc.scoped)
			sess, err := svc.CreateSession(session.WithPrincipal(t.Context(), alice), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			auth := server.NewAuthenticator(server.SecurityConfig{Validator: fakeValidator{ok: map[string]session.Principal{
				"alice-tok": *alice,
				"bob-tok":   *bob,
			}}})
			client, cleanup := dialGRPCSecure(t, svc, auth)
			defer cleanup()
			stream, err := client.Converse(bearerCtx(t.Context(), "alice-tok"))
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "go"}}}); err != nil {
				t.Fatal(err)
			}
			var runID, askID, reviewID string
			for askID == "" {
				response, err := stream.Recv()
				if err != nil {
					t.Fatalf("run stream closed before ask: %v", err)
				}
				if ev := response.GetEvent(); ev.GetType() == string(session.EvPermissionAsk) {
					if (ev.GetAsk().GetGuardrail() != nil) != tc.scoped {
						t.Fatalf("ask guardrail scope = %+v, scoped = %t", ev.GetAsk().GetGuardrail(), tc.scoped)
					}
					runID, askID, reviewID = ev.GetRunId(), ev.GetAsk().GetAskId(), ev.GetAsk().GetGuardrail().GetReviewId()
				}
			}
			if err := stream.CloseSend(); err != nil {
				t.Fatal(err)
			}
			request := &mecatlv1.ResolveRunAskRequest{SessionId: string(sess.ID), ExpectedRunId: runID, AskId: askID, Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY}
			if tc.scoped {
				request.ReviewId = reviewID
				request.GuardrailKind = mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_ACTION
			}
			if _, err := client.ResolveRunAsk(bearerCtx(t.Context(), "bob-tok"), request); err != nil {
				t.Fatal(err)
			}
			for {
				_, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			originSeen := false
			for _, ev := range readEventLog(t, log, sess.ID) {
				if ev.Type == session.EvPermissionAsk && ev.RunID == runID && ev.Ask != nil && ev.Ask.AskID == askID {
					originSeen = true
					if ev.Ask.Origin != tc.origin {
						t.Fatalf("durable ask origin = %q, want %q", ev.Ask.Origin, tc.origin)
					}
				}
			}
			if !originSeen {
				t.Fatal("exact ask was not recorded")
			}
			assertExactApprovalActor(t, log, sess.ID, runID, askID, bob)
		})
	}

	t.Run("ordinary direct verdict without caller", func(t *testing.T) {
		svc, log := exactRunActorService(t, false)
		starter := session.WithPrincipal(t.Context(), alice)
		sess, err := svc.CreateSession(starter, session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		run, err := svc.StartRunContent(starter, sess.ID, "go", nil)
		if err != nil {
			t.Fatal(err)
		}
		recorder := server.NewRunEventRecorder(context.WithoutCancel(starter), svc, sess.ID)
		var askID string
		for ev := range run.Events() {
			recorder.Observe(ev)
			if ev.Type == session.EvPermissionAsk {
				askID = ev.Ask.AskID
				break
			}
		}
		if askID == "" {
			t.Fatal("live run did not park on an ordinary ask")
		}
		svc.Persist(starter, sess.ID)
		if _, err := svc.ResolveRunAsk(t.Context(), sess.ID, run.RunID(), askID, session.VerdictDeny); err != nil {
			t.Fatal(err)
		}
		for ev := range run.Events() {
			recorder.Observe(ev)
		}
		recorder.Close()
		svc.FinishRun(sess.ID, run)
		assertExactApprovalActor(t, log, sess.ID, run.RunID(), askID, nil)
	})

	t.Run("later in-stream approval keeps starter", func(t *testing.T) {
		svc, log := exactRunActorService(t, false,
			mockllm.ToolCallTurn(call("w1", "Write", `{"path":"a.go"}`)),
			mockllm.ToolCallTurn(call("w2", "Write", `{"path":"b.go"}`)),
			mockllm.TextTurn("done"),
		)
		starter := session.WithPrincipal(t.Context(), alice)
		verdictCaller := session.WithPrincipal(t.Context(), bob)
		sess, err := svc.CreateSession(starter, session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		run, err := svc.StartRunContent(starter, sess.ID, "go", nil)
		if err != nil {
			t.Fatal(err)
		}
		recorder := server.NewRunEventRecorder(context.WithoutCancel(starter), svc, sess.ID)
		var firstAsk, secondAsk string
		for ev := range run.Events() {
			recorder.Observe(ev)
			if ev.Type != session.EvPermissionAsk || ev.Ask == nil {
				continue
			}
			svc.Persist(starter, sess.ID)
			if firstAsk == "" {
				firstAsk = ev.Ask.AskID
				if _, err := svc.ResolveRunAsk(verdictCaller, sess.ID, run.RunID(), firstAsk, session.VerdictDeny); err != nil {
					t.Fatal(err)
				}
			} else {
				secondAsk = ev.Ask.AskID
				if _, err := svc.ApproveRun(starter, sess.ID, secondAsk, session.VerdictDeny, run.RunID()); err != nil {
					t.Fatal(err)
				}
			}
		}
		recorder.Close()
		svc.FinishRun(sess.ID, run)
		if firstAsk == "" || secondAsk == "" || firstAsk == secondAsk {
			t.Fatalf("sequential asks = %q, %q; want two distinct asks", firstAsk, secondAsk)
		}
		assertExactApprovalActor(t, log, sess.ID, run.RunID(), firstAsk, bob)
		secondApprovals := 0
		for _, ev := range readEventLog(t, log, sess.ID) {
			if ev.Type == session.EvApproval && ev.RunID == run.RunID() && ev.Approval != nil && ev.Approval.AskID == secondAsk {
				secondApprovals++
				if got := ownerOf(ev.Actor); got != *alice {
					t.Fatalf("in-stream approval actor = %+v, want starter %+v", ev.Actor, *alice)
				}
			}
		}
		if secondApprovals != 1 {
			t.Fatalf("in-stream approvals = %d, want one", secondApprovals)
		}
	})
}
