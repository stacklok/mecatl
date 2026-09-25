package server_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type nativePlanTerminalLog struct {
	port.EventLog
	entered chan struct{}
	release chan struct{}
}

func (l *nativePlanTerminalLog) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopEndTurn {
		close(l.entered)
		<-l.release
	}
	return l.EventLog.Append(ctx, id, ev)
}

func TestNativePersistedExactPlanClaimLifecycle(t *testing.T) {
	for _, outcome := range []string{"allow", "acquisition failure", "cancel before acceptance"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"}
				provider := &nativeRunProvider{ref: ref, exclusive: true, renewed: make(chan struct{}, 2), renewalDeadline: time.Now().Add(time.Minute)}
				var renewExited atomic.Int64
				provider.renewHook = func(ctx context.Context) error {
					<-ctx.Done()
					renewExited.Add(1)
					return ctx.Err()
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				acquireErr := errors.New("native claim unavailable")
				if outcome == "acquisition failure" {
					provider.acquireErr = acquireErr
				} else if outcome == "cancel before acceptance" {
					provider.acquireHook = cancel
				}
				store := memstore.New()
				parked := makePlanControlSession(t, "native-plan-restored")
				parked.EnvironmentRef = ref
				if err := store.Save(ctx, parked); err != nil {
					t.Fatal(err)
				}
				log := &blockingPlanResultLog{serverLog: memstore.NewEventLog(), seen: make(chan struct{}), release: make(chan struct{})}
				allowPlanDrain := sync.OnceFunc(func() { close(log.release) })
				llm := mockllm.New(mockllm.TextTurn("executed"))
				cat := tool.NewCatalog()
				cat.MustRegister(agent.NewPresentPlanTool())
				engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model", Interactive: true, Store: store})
				svc, err := newPlacementTestService(server.Config{
					Engine: engine, Store: store, EventLog: log, PlacementProvider: provider, PlacementScope: "test",
					ExecutionAccess: provider, SharedEngineRoot: "/native",
				})
				if err != nil {
					t.Fatal(err)
				}
				defer svc.Close()
				defer allowPlanDrain()
				ack, err := svc.ResolvePlanAsk(ctx, parked.ID, parked.RunID(), "plan-ask", session.VerdictAllowOnce)
				if outcome != "allow" {
					wantErr, wantReleases := acquireErr, int64(0)
					if outcome == "cancel before acceptance" {
						wantErr, wantReleases = context.Canceled, 1
					}
					if !errors.Is(err, wantErr) || provider.acquires.Load() != 1 || provider.releases.Load() != wantReleases || provider.held.Load() || llm.Calls() != 0 {
						t.Fatalf("failed admission: err=%v acquires=%d releases=%d held=%t modelCalls=%d", err, provider.acquires.Load(), provider.releases.Load(), provider.held.Load(), llm.Calls())
					}
					stored, err := store.Load(t.Context(), parked.ID)
					if err != nil {
						t.Fatal(err)
					}
					ask, ok := stored.PendingAsk()
					if stored.State != session.StateAwaiting || stored.RunID() != parked.RunID() || !ok || ask.AskID != "plan-ask" || ask.Origin != session.ApprovalOriginPlan {
						t.Fatalf("failed admission consumed durable plan ask: state=%s run=%s ask=%+v", stored.State, stored.RunID(), ask)
					}
					return
				}
				if err != nil || ack.RunID != parked.RunID() || ack.AskID != "plan-ask" {
					t.Fatalf("restored exact acceptance = %+v, %v", ack, err)
				}
				cancel() // Accepted work belongs to the Service, not the unary caller.
				<-log.seen
				if provider.acquires.Load() != 1 || provider.releases.Load() != 0 || !provider.held.Load() {
					t.Fatalf("restored plan before relay drain: acquires=%d releases=%d held=%t", provider.acquires.Load(), provider.releases.Load(), provider.held.Load())
				}
				<-provider.renewed
				allowPlanDrain()
				synctest.Wait()
				if provider.acquires.Load() != 2 || provider.releases.Load() != 2 || provider.held.Load() || renewExited.Load() != 1 || llm.Calls() != 1 {
					t.Fatalf("restored continuation: acquires=%d releases=%d held=%t renewExited=%d modelCalls=%d", provider.acquires.Load(), provider.releases.Load(), provider.held.Load(), renewExited.Load(), llm.Calls())
				}
				stored, err := store.Load(t.Context(), parked.ID)
				if err != nil {
					t.Fatal(err)
				}
				if stored.State != session.StateCompleted || stored.RunID() == parked.RunID() || stored.Mode != session.ModeDefault {
					t.Fatalf("restored continuation state=%s run=%s mode=%s", stored.State, stored.RunID(), stored.Mode)
				}
			})
		})
	}
}

func TestNativeExactPlanClaimLifecycle(t *testing.T) {
	for _, outcome := range []string{"allow", "deny", "owner cancel", "drain after acceptance", "release failure"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"}
				provider := &nativeRunProvider{ref: ref, exclusive: true, renewed: make(chan struct{}, 2), renewalDeadline: time.Now().Add(time.Minute)}
				var renewExited, pins, unpins atomic.Int64
				provider.renewHook = func(ctx context.Context) error {
					<-ctx.Done()
					renewExited.Add(1)
					return ctx.Err()
				}
				releaseEntered, releaseAllowed := make(chan struct{}), make(chan struct{})
				allowRelease := sync.OnceFunc(func() { close(releaseAllowed) })
				provider.releaseHook = func(context.Context) error {
					if provider.releases.Load() == 0 {
						if renewExited.Load() != 1 || pins.Load()-unpins.Load() != 1 {
							t.Errorf("old release before renewal joined or operation pin lost: renewExited=%d pins=%d unpins=%d", renewExited.Load(), pins.Load(), unpins.Load())
						}
						close(releaseEntered)
						<-releaseAllowed
						if outcome == "release failure" {
							return errors.New("release uncertain; native claim remains held")
						}
					}
					return nil
				}
				store := memstore.New()
				log := &nativePlanTerminalLog{EventLog: memstore.NewEventLog(), entered: make(chan struct{}), release: make(chan struct{})}
				allowTerminal := sync.OnceFunc(func() { close(log.release) })
				llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"plan":"one"}`)), mockllm.TextTurn("executed"))
				cat := tool.NewCatalog()
				cat.MustRegister(agent.NewPresentPlanTool())
				engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model", Interactive: true, Store: store})
				svc, err := newPlacementTestService(server.Config{
					Engine: engine, Store: store, EventLog: log, PlacementProvider: provider, PlacementScope: "test",
					ExecutionAccess: provider, SharedEngineRoot: "/native", OwnershipEnforced: true,
					OperationPin: func(ctx context.Context) (context.Context, func(), error) {
						pins.Add(1)
						return ctx, func() { unpins.Add(1) }, nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer svc.Close()
				defer allowRelease()
				defer allowTerminal()
				owner := &session.Principal{Issuer: "test", Subject: "owner"}
				ctx := session.WithPrincipal(t.Context(), owner)
				sess, err := svc.CreateSession(ctx, session.ModePlan, session.Limits{MaxTurns: 5})
				if err != nil {
					t.Fatal(err)
				}
				run, err := svc.StartInteractiveRunContentWithPlanContinuation(ctx, sess.ID, "plan this", nil)
				if err != nil {
					t.Fatal(err)
				}
				askID, _ := awaitLivePlanAsk(t, run, "c1", "one")
				svc.Persist(ctx, sess.ID)
				<-provider.renewed
				foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: "foreign"})
				if _, err := svc.ResolvePlanAsk(foreign, sess.ID, run.RunID(), askID, session.VerdictAllowOnce); !errors.Is(err, server.ErrNotFound) {
					t.Fatalf("foreign verdict = %v", err)
				}
				if _, err := svc.ResolvePlanAsk(ctx, sess.ID, "stale-run", askID, session.VerdictAllowOnce); !errors.Is(err, server.ErrStaleRunControl) {
					t.Fatalf("stale run = %v", err)
				}
				if _, err := svc.ResolvePlanAsk(ctx, sess.ID, run.RunID(), "wrong-ask", session.VerdictAllowOnce); !errors.Is(err, server.ErrAskNotPending) {
					t.Fatalf("wrong ask = %v", err)
				}
				if outcome == "owner cancel" {
					if _, err := svc.CancelRun(ctx, sess.ID, run.RunID()); err != nil {
						t.Fatal(err)
					}
				} else {
					verdict := session.VerdictAllowOnce
					if outcome == "deny" {
						verdict = session.VerdictDeny
					}
					ack, err := svc.ResolvePlanAsk(ctx, sess.ID, run.RunID(), askID, verdict)
					if err != nil || ack.RunID != run.RunID() || ack.AskID != askID {
						t.Fatalf("exact owner acceptance = %+v, %v", ack, err)
					}
				}
				if provider.releases.Load() != 0 || !provider.held.Load() {
					t.Fatal("planning claim released before relay drain")
				}
				for range run.Events() {
				}
				svc.Persist(ctx, sess.ID)
				if outcome == "allow" || outcome == "release failure" {
					if _, err := svc.StartRunContent(ctx, sess.ID, "competing prompt", nil); !errors.Is(err, server.ErrFailedPrecondition) {
						t.Fatalf("competing prompt = %v", err)
					}
					if _, err := svc.RetryFailedRun(ctx, sess.ID); !errors.Is(err, server.ErrFailedPrecondition) || errors.Is(err, server.ErrFailedStepRetryIneligible) {
						t.Fatalf("competing retry bypassed reservation: %v", err)
					}
				}
				if outcome == "drain after acceptance" {
					svc.Drain()
				}
				finished := make(chan struct{})
				go func() {
					svc.FinishRun(sess.ID, run)
					close(finished)
				}()
				synctest.Wait()
				select {
				case <-releaseEntered:
				default:
					t.Fatal("drained plan did not join renewal and release its native claim")
				}
				// Provider I/O cannot hold s.mu; the old registry entry and operation
				// pin must remain until release completes, behind the entry barrier.
				if !svc.IsLive(sess.ID) || provider.acquires.Load() != 1 || provider.releases.Load() != 0 {
					t.Fatal("old lifecycle retired or continuation acquired during blocked release")
				}
				allowRelease()
				<-finished
				synctest.Wait()
				if outcome == "allow" {
					select {
					case <-log.entered:
					default:
						t.Fatal("accepted native continuation did not reach its terminal relay")
					}
					if provider.acquires.Load() != 2 || provider.releases.Load() != 1 || !provider.held.Load() || unpins.Load() != 1 {
						t.Fatalf("continuation before drain: acquires=%d releases=%d held=%t unpins=%d", provider.acquires.Load(), provider.releases.Load(), provider.held.Load(), unpins.Load())
					}
					svc.FinishRun(sess.ID, run) // stale old relay must not retire its successor.
					if !svc.IsLive(sess.ID) || provider.releases.Load() != 1 {
						t.Fatal("old FinishRun removed the continuation")
					}
					allowTerminal()
					synctest.Wait()
				}
				wantAcquires, wantReleases, wantCalls := int64(1), int64(1), 1
				if outcome == "allow" {
					wantAcquires, wantReleases, wantCalls = 2, 2, 2
				} else if outcome == "release failure" {
					wantAcquires, wantReleases = 2, 0
				}
				if provider.acquires.Load() != wantAcquires || provider.releases.Load() != wantReleases || provider.held.Load() != (outcome == "release failure") || llm.Calls() != wantCalls || pins.Load() != unpins.Load() {
					t.Fatalf("settled: acquires=%d releases=%d held=%t modelCalls=%d pins=%d unpins=%d", provider.acquires.Load(), provider.releases.Load(), provider.held.Load(), llm.Calls(), pins.Load(), unpins.Load())
				}
				failed := false
				for ev, err := range log.Read(ctx, sess.ID) {
					if err != nil {
						t.Fatal(err)
					}
					failed = failed || ev.Type == session.EvPlanContinuationFailed
				}
				if failed != (outcome == "release failure" || outcome == "drain after acceptance") {
					t.Fatalf("continuation failure recorded = %t for %s", failed, outcome)
				}
			})
		})
	}
}
