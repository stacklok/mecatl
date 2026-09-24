package server_test

import (
	"context"
	"errors"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func newControlLifecycleService(
	t *testing.T,
	store port.SessionStore,
	llm port.LLMProvider,
	ran *atomic.Int64,
	lease port.SessionLease,
	await func(context.Context, string, string) error,
) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	cat.MustRegister(&writeAskTool{ran: ran})
	eng := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, permstore.New()),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: eng, Store: store, SessionLease: lease, LeaseOwner: "control-lifecycle",
		LeaseTTL: time.Hour, LeaseRenewInterval: 30 * time.Minute,
		AwaitContextWindow: await,
		Now:                func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func makePlanControlSession(t *testing.T, id session.SessionID) *session.Session {
	t.Helper()
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	sess := session.New(id, session.ModePlan, ref, session.Limits{}, time.Unix(0, 0))
	sess.BeginRun("run-" + string(id))
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("plan-call", "PresentPlan", []byte(`{"plan":"one"}`))
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	if err := sess.PauseForApproval(session.PendingAsk{
		AskID: "plan-ask", Tool: "PresentPlan", Call: call.ID, Origin: session.ApprovalOriginPlan,
	}); err != nil {
		t.Fatal(err)
	}
	return sess
}

func makePersistedControlSession(t *testing.T, id session.SessionID) (*session.Session, session.PendingAsk) {
	t.Helper()
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	sess := session.New(id, session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
	sess.BeginRun("run-" + string(id))
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("durable-call", "Write", []byte(`{}`))
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	ask := session.PendingAsk{AskID: "durable-ask", Tool: "Write", Call: call.ID, Origin: session.ApprovalOriginPermission}
	if err := sess.PauseForApproval(ask); err != nil {
		t.Fatal(err)
	}
	return sess, ask
}

func makePersistedScopedControlSession(t *testing.T, id session.SessionID, kind session.GuardrailApprovalKind) (*session.Session, session.PendingAsk) {
	t.Helper()
	sess, _ := makePersistedControlSession(t, id)
	if _, err := sess.ResumeWith(); err != nil {
		t.Fatal(err)
	}
	ask := session.PendingAsk{
		AskID: "scoped-ask", Tool: "Write", Call: "durable-call", Origin: session.ApprovalOriginHookGuardrail,
		Guardrail: &session.GuardrailPendingScope{ReviewID: "original-review", Kind: kind},
	}
	if err := sess.PauseForApproval(ask); err != nil {
		t.Fatal(err)
	}
	return sess, ask
}

func TestSDKRunControls_DrainingStillConcealsUnknownAndForeignSessions(t *testing.T) {
	store := memstore.New()
	eng := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(), Catalog: tool.NewCatalog(),
		Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: eng, Store: store, OwnershipEnforced: true,
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	owned, err := svc.CreateSession(session.WithPrincipal(t.Context(), alice), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	svc.Drain()
	for name, tc := range map[string]struct {
		ctx context.Context
		id  session.SessionID
	}{
		"unknown": {ctx: session.WithPrincipal(t.Context(), alice), id: "missing"},
		"foreign": {ctx: session.WithPrincipal(t.Context(), bob), id: owned.ID},
	} {
		t.Run(name, func(t *testing.T) {
			_, resolveErr := svc.ResolveRunAsk(tc.ctx, tc.id, "run", "ask", session.VerdictAllowOnce)
			if !errors.Is(resolveErr, server.ErrNotFound) {
				t.Fatalf("ResolveRunAsk while draining = %v, want concealed ErrNotFound", resolveErr)
			}
		})
	}
}

func TestSDKRunControls_PersistedPlanAskRefusesWithoutRehydration(t *testing.T) {
	store := memstore.New()
	parked := makePlanControlSession(t, "persisted-plan-control")
	if err := store.Save(t.Context(), parked); err != nil {
		t.Fatal(err)
	}
	var ran atomic.Int64
	svc := newControlLifecycleService(t, store, mockllm.New(mockllm.TextTurn("must not run")), &ran, nil, nil)
	t.Cleanup(svc.Close)
	_, err := svc.ResolveRunAsk(t.Context(), parked.ID, parked.RunID(), "plan-ask", session.VerdictAllowOnce)
	if !errors.Is(err, server.ErrPlanResolutionRequired) {
		t.Fatalf("ResolveRunAsk plan origin = %v, want ErrPlanResolutionRequired", err)
	}
	if _, live := svc.LookupRun(parked.ID); live {
		t.Fatal("plan-originated ask installed a resumed run")
	}
	if ran.Load() != 0 {
		t.Fatalf("tool executions = %d, want 0", ran.Load())
	}
	got, err := store.Load(t.Context(), parked.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending, ok := got.PendingAsk()
	if got.State != session.StateAwaiting || got.Mode != session.ModePlan || !ok || pending.AskID != "plan-ask" || pending.Origin != session.ApprovalOriginPlan {
		t.Fatalf("persisted plan mutated: state=%q mode=%q pending=%+v ok=%t", got.State, got.Mode, pending, ok)
	}
}

func TestSDKRunControls_PersistedResolveHonorsPreAcceptanceCancellationAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{name: "cancellation", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}, want: context.Canceled},
		{name: "deadline", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 25*time.Millisecond)
		}, want: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			parked, ask := makePersistedControlSession(t, session.SessionID("pre-accept-"+tc.name))
			if err := store.Save(t.Context(), parked); err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			var once sync.Once
			await := func(ctx context.Context, _, _ string) error {
				once.Do(func() { close(entered) })
				<-ctx.Done()
				return ctx.Err()
			}
			var ran atomic.Int64
			svc := newControlLifecycleService(t, store, mockllm.New(mockllm.TextTurn("must not run")), &ran, nil, await)
			t.Cleanup(svc.Close)
			ctx, cancel := tc.ctx()
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, resolveErr := svc.ResolveRunAsk(ctx, parked.ID, parked.RunID(), ask.AskID, session.VerdictAllowOnce)
				result <- resolveErr
			}()
			<-entered
			if tc.name == "cancellation" {
				cancel()
			}
			if err := <-result; !errors.Is(err, tc.want) {
				t.Fatalf("ResolveRunAsk = %v, want %v", err, tc.want)
			}
			if ran.Load() != 0 {
				t.Fatalf("tool executions = %d, want 0", ran.Load())
			}
			if _, live := svc.LookupRun(parked.ID); live {
				t.Fatal("cancelled pre-acceptance resolution left a live run")
			}
			got, err := store.Load(t.Context(), parked.ID)
			if err != nil {
				t.Fatal(err)
			}
			pending, ok := got.PendingAsk()
			if got.State != session.StateAwaiting || !ok || pending.AskID != ask.AskID {
				t.Fatalf("persisted handoff changed: state=%q pending=%+v ok=%t", got.State, pending, ok)
			}
		})
	}
}

func TestSDKRunControls_PersistedResolveReloadsAfterLeaseAcquire(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   session.GuardrailApprovalKind
		scoped bool
	}{
		{name: "ordinary"},
		{name: "contextual action", kind: session.GuardrailApprovalAction, scoped: true},
		{name: "contextual result release", kind: session.GuardrailApprovalResultRelease, scoped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			var parked *session.Session
			var ask session.PendingAsk
			if tc.scoped {
				parked, ask = makePersistedScopedControlSession(t, session.SessionID("post-lease-stale-"+tc.name), tc.kind)
			} else {
				parked, ask = makePersistedControlSession(t, "post-lease-stale-control")
			}
			if err := store.Save(t.Context(), parked); err != nil {
				t.Fatal(err)
			}
			lease := &fakeLease{}
			lease.acquireHook = func(id session.SessionID, _ string) {
				fresh, err := store.Load(context.Background(), id)
				if err != nil {
					panic(err)
				}
				if _, err := fresh.ResumeWith(); err != nil {
					panic(err)
				}
				replacement := session.PendingAsk{AskID: "replacement-ask", Tool: "Write", Call: "durable-call", Origin: session.ApprovalOriginPermission}
				if tc.scoped {
					replacement = ask
					replacement.Guardrail = &session.GuardrailPendingScope{ReviewID: "replacement-review", Kind: tc.kind}
				}
				if err := fresh.PauseForApproval(replacement); err != nil {
					panic(err)
				}
				if err := store.Save(context.Background(), fresh); err != nil {
					panic(err)
				}
			}
			var ran atomic.Int64
			svc := newControlLifecycleService(t, store, mockllm.New(mockllm.TextTurn("must not run")), &ran, lease, nil)
			t.Cleanup(svc.Close)
			var err error
			if tc.scoped {
				_, err = svc.ResolveScopedRunAsk(t.Context(), parked.ID, parked.RunID(), agent.ApprovalResolution{
					AskID: ask.AskID, ReviewID: ask.Guardrail.ReviewID, Kind: tc.kind, Verdict: session.VerdictAllowOnce,
				})
				if !errors.Is(err, agent.ErrApprovalIntentMismatch) {
					t.Fatalf("ResolveScopedRunAsk over stale pre-lease scope = %v, want ErrApprovalIntentMismatch", err)
				}
			} else {
				_, err = svc.ResolveRunAsk(t.Context(), parked.ID, parked.RunID(), ask.AskID, session.VerdictAllowOnce)
				if !errors.Is(err, server.ErrAskNotPending) {
					t.Fatalf("ResolveRunAsk over stale pre-lease snapshot = %v, want ErrAskNotPending", err)
				}
			}
			if ran.Load() != 0 {
				t.Fatalf("stale allow-once executed the tool %d time(s)", ran.Load())
			}
			if _, live := svc.LookupRun(parked.ID); live {
				t.Fatal("stale pre-lease snapshot left a resumed run")
			}
			got, err := store.Load(t.Context(), parked.ID)
			if err != nil {
				t.Fatal(err)
			}
			pending, ok := got.PendingAsk()
			if !ok || got.State != session.StateAwaiting {
				t.Fatalf("replacement was not preserved: state=%q pending=%+v ok=%t", got.State, pending, ok)
			}
			if tc.scoped {
				if pending.AskID != ask.AskID || pending.Guardrail == nil || pending.Guardrail.ReviewID != "replacement-review" || pending.Guardrail.Kind != tc.kind {
					t.Fatalf("scoped replacement changed: %+v", pending)
				}
			} else if pending.AskID != "replacement-ask" {
				t.Fatalf("ordinary replacement changed: %+v", pending)
			}
		})
	}
}

type closeJoinProvider struct {
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func (p *closeJoinProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		close(p.entered)
		<-ctx.Done()
		close(p.cancelled)
		<-p.release
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopCancelled}, nil)
	}, nil
}

func (*closeJoinProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func TestSDKRunControls_CloseJoinsDetachedControlRelay(t *testing.T) {
	store := memstore.New()
	parked, ask := makePersistedControlSession(t, "close-joins-detached-control")
	if err := store.Save(t.Context(), parked); err != nil {
		t.Fatal(err)
	}
	provider := &closeJoinProvider{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(provider.release) })
	var ran atomic.Int64
	svc := newControlLifecycleService(t, store, provider, &ran, nil, nil)
	if _, err := svc.ResolveRunAsk(t.Context(), parked.ID, parked.RunID(), ask.AskID, session.VerdictAllowOnce); err != nil {
		t.Fatalf("ResolveRunAsk: %v", err)
	}
	<-provider.entered
	closed := make(chan struct{})
	go func() {
		svc.Close()
		close(closed)
	}()
	<-provider.cancelled
	select {
	case <-closed:
		t.Fatal("Service.Close returned before its detached control relay settled")
	case <-time.After(25 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(provider.release) })
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Service.Close did not join the released detached control relay")
	}
	if _, live := svc.LookupRun(parked.ID); live {
		t.Fatal("Service.Close returned with the detached run still registered")
	}
}

func TestSDKRunControls_GRPCContextualResolveDetachesAndShutdownJoins(t *testing.T) {
	store := memstore.New()
	parked, ask := makePersistedScopedControlSession(t, "grpc-contextual-detached-control", session.GuardrailApprovalAction)
	if err := store.Save(t.Context(), parked); err != nil {
		t.Fatal(err)
	}
	provider := &closeJoinProvider{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(provider.release) })
	var ran atomic.Int64
	svc := newControlLifecycleService(t, store, provider, &ran, nil, nil)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	response, err := client.ResolveRunAsk(t.Context(), &mecatlv1.ResolveRunAskRequest{
		SessionId: string(parked.ID), ExpectedRunId: parked.RunID(), AskId: ask.AskID,
		ReviewId: ask.Guardrail.ReviewID, GuardrailKind: mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_ACTION,
		Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE,
	})
	if err != nil {
		t.Fatalf("ResolveRunAsk: %v", err)
	}
	if response.GetRunId() != parked.RunID() || response.GetAskId() != ask.AskID {
		t.Fatalf("ResolveRunAsk acknowledgement = %+v", response)
	}
	<-provider.entered
	if _, live := svc.LookupRun(parked.ID); !live {
		t.Fatal("successful unary response cancelled the contextual continuation")
	}

	closed := make(chan struct{})
	go func() {
		svc.Close()
		close(closed)
	}()
	<-provider.cancelled
	select {
	case <-closed:
		t.Fatal("Service.Close returned before the contextual relay settled")
	case <-time.After(25 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(provider.release) })
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Service.Close did not join the contextual relay")
	}
	if _, live := svc.LookupRun(parked.ID); live {
		t.Fatal("Service.Close returned with the contextual run still registered")
	}
}

func TestSDKRunControls_ClosePreservesDetachedAwaitingHandoff(t *testing.T) {
	store := memstore.New()
	parked, firstAsk := makePersistedControlSession(t, "close-preserves-detached-awaiting")
	if err := store.Save(t.Context(), parked); err != nil {
		t.Fatal(err)
	}
	var ran atomic.Int64
	svc := newControlLifecycleService(t, store, mockllm.New(mockllm.ToolCallTurn(
		session.NewToolCall("second-call", "Write", []byte(`{}`)),
	)), &ran, nil, nil)
	if _, err := svc.ResolveRunAsk(t.Context(), parked.ID, parked.RunID(), firstAsk.AskID, session.VerdictAllowOnce); err != nil {
		t.Fatalf("ResolveRunAsk: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	var secondAsk session.PendingAsk
	for time.Now().Before(deadline) {
		got, err := store.Load(t.Context(), parked.ID)
		if err == nil && got.State == session.StateAwaiting {
			if pending, ok := got.PendingAsk(); ok && pending.AskID != firstAsk.AskID {
				secondAsk = pending
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if secondAsk.AskID == "" {
		t.Fatal("detached continuation did not persist its second awaiting handoff")
	}
	svc.Close()
	settleDeadline := time.Now().Add(time.Second)
	for time.Now().Before(settleDeadline) {
		if _, live := svc.LookupRun(parked.ID); !live {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, live := svc.LookupRun(parked.ID); live {
		t.Fatal("shutdown-cancelled detached awaiting run did not settle")
	}
	got, err := store.Load(t.Context(), parked.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending, ok := got.PendingAsk()
	if got.State != session.StateAwaiting || !ok || pending.AskID != secondAsk.AskID {
		t.Fatalf("Close overwrote detached awaiting handoff: state=%q pending=%+v ok=%t", got.State, pending, ok)
	}
}
