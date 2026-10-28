package app

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type contextCheckingScheduleStore struct{ port.ScheduleStore }

func (s contextCheckingScheduleStore) RecordFire(ctx context.Context, fire port.ScheduleFire) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.ScheduleStore.RecordFire(ctx, fire)
}

type deadlineDiagnostics struct{ messages chan string }

func (d *deadlineDiagnostics) With(...any) port.Diagnostics { return d }
func (d *deadlineDiagnostics) Log(_ context.Context, _ port.Level, msg string, _ ...any) {
	select {
	case d.messages <- msg:
	default:
	}
}

func waitDeadlineGate(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("deadline fixture gate not reached")
	}
}

func waitDeadlineDiagnostic(t *testing.T, diag *deadlineDiagnostics, fragment string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case msg := <-diag.messages:
			if strings.Contains(msg, fragment) {
				return
			}
		case <-timer.C:
			t.Fatalf("missing diagnostic %q", fragment)
		}
	}
}

func TestFireDeadlineCancellationFailureAndBlockedCancellation(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "blocked"}[blocked], func(t *testing.T) {
			ignore := goleak.IgnoreCurrent()
			t.Cleanup(func() { goleak.VerifyNone(t, ignore) })
			diag := &deadlineDiagnostics{messages: make(chan string, 8)}
			entered, release, joined := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			timedOut, stop := watchFireDeadline(context.Background(), diag, port.ScheduleFire{
				ID: "fixture", ScheduleName: "deadline", Deadline: time.Now().Add(-time.Second),
			}, func() error {
				close(entered)
				if blocked {
					<-release
				}
				return errors.New("cancellation fixture failure")
			})
			t.Cleanup(func() { once.Do(func() { close(release) }); stop() })
			waitDeadlineGate(t, entered)
			waitDeadlineDiagnostic(t, diag, "termination unconfirmed")
			if !timedOut.Load() {
				t.Fatal("deadline not observed independently of cancellation")
			}
			go func() { stop(); close(joined) }()
			if blocked {
				select {
				case <-joined:
					t.Fatal("blocked cancellation worker abandoned")
				case <-time.After(20 * time.Millisecond):
				}
			}
			once.Do(func() { close(release) })
			waitDeadlineGate(t, joined)
			waitDeadlineDiagnostic(t, diag, "cancellation failed")
			stop() // repeat stop must not wait on a timer callback that never ran
		})
	}
}

type uncooperativeFireProvider struct {
	entered, release             chan struct{}
	originEntered, originRelease chan struct{}
	calls                        atomic.Int32
}

func (*uncooperativeFireProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}
func (p *uncooperativeFireProvider) Stream(_ context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	call := p.calls.Add(1)
	if p.originEntered != nil {
		if call == 1 {
			return func(yield func(port.Chunk, error) bool) {
				close(p.originEntered)
				<-p.originRelease
				yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
			}, nil
		}
		call--
	}
	if call > 1 {
		return func(yield func(port.Chunk, error) bool) {
			yield(port.Chunk{Kind: port.ChunkText, Text: "acknowledged"}, nil)
			yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
		}, nil
	}
	return func(yield func(port.Chunk, error) bool) {
		close(p.entered)
		<-p.release // Events cannot close until the producer returns.
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopCancelled}, nil)
	}, nil
}

func TestFireDeadlineNonclosingEventsKeepLeaseUntilRecovery(t *testing.T) {
	testFireRecovery(t, false, false)
}

func TestFireDeadlineJoinsBlockedCancelWithBusyOrigin(t *testing.T) {
	testFireRecovery(t, true, false)
}

func TestFireStopGraceKeepsLeaseUntilRecovery(t *testing.T) {
	testFireRecovery(t, false, true)
}

func testFireRecovery(t *testing.T, blockedCancel, stopBeforeRecovery bool) {
	t.Helper()
	ignore := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, ignore) })
	ctx := context.Background()
	callerCtx, cancelCaller := context.WithCancel(ctx)
	defer cancelCaller()
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	llm := &uncooperativeFireProvider{entered: make(chan struct{}), release: make(chan struct{})}
	if blockedCancel {
		llm.originEntered, llm.originRelease = make(chan struct{}), make(chan struct{})
	}
	diag := &deadlineDiagnostics{messages: make(chan string, 32)}
	lease := memlease.New(wallclock.Clock{}, 30*time.Second)
	queue := NewInMemoryDeliveryQueue(WithDeliveryDiagnostics(diag))
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test", Store: store, DeliveryQueue: queue})
	svc, err := newTestServerService(server.Config{Engine: engine, Store: store, Now: time.Now, EventLog: store, Diagnostics: diag, SessionLease: lease, LeaseOwner: "fixture", LeaseTTL: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	origin, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	schedStore := contextCheckingScheduleStore{store.ScheduleStore()}
	cancelEntered, cancelRelease := make(chan struct{}), make(chan struct{})
	var cancelOnce, originOnce sync.Once
	cancelRun := svc.Cancel
	if blockedCancel {
		cancelRun = func(context.Context, session.SessionID, string) error {
			close(cancelEntered)
			<-cancelRelease
			return nil
		}
	}
	fireFunc := makeFireFunc(svc, schedStore, time.Second, nil)
	if blockedCancel {
		fireFunc = makeFireFuncWithCancel(svc, schedStore, time.Second, nil, cancelRun)
	}
	s := scheduler.New(scheduler.Config{Store: schedStore, Clock: wallclock.Clock{}, Fire: fireFunc, MaxConcurrentFires: 1, Diagnostics: diag, StopFireGrace: 50 * time.Millisecond, DeliverFireResult: deliverFireResult(svc, queue)})
	done := make(chan struct{})
	var originRun *agent.Run
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(llm.release) })
		cancelOnce.Do(func() { close(cancelRelease) })
		if blockedCancel {
			originOnce.Do(func() { close(llm.originRelease) })
		}
		waitDeadlineGate(t, done)
		if originRun != nil {
			drainDeliveryRun(svc, origin.ID, originRun)
		}
		_ = s.Stop()
		svc.Close()
	})
	if blockedCancel {
		originRun, err = svc.StartRunContent(ctx, origin.ID, "busy origin", nil)
		if err != nil {
			t.Fatal(err)
		}
		waitDeadlineGate(t, llm.originEntered)
	}
	now := time.Now()
	if err := schedStore.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: "uncooperative", OriginSessionID: origin.ID, Prompt: "wait", Trigger: port.TriggerSpec{Cron: "* * * * *"}, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"}, PlacementScope: "legacy-local", FireTimeout: time.Second}, State: port.ScheduleState{Enabled: true, NextFireAt: now}}); err != nil {
		t.Fatal(err)
	}
	var fire port.ScheduleFire
	var fireErr error
	go func() { defer close(done); fire, fireErr = s.FireNow(callerCtx, "uncooperative", now) }()
	waitDeadlineGate(t, llm.entered)
	waitDeadlineDiagnostic(t, diag, "termination unconfirmed")
	loaded, err := schedStore.Load(ctx, "uncooperative")
	if err != nil {
		t.Fatal(err)
	}
	inflight, err := schedStore.LoadFire(ctx, string(loaded.State.LastFireSessionID))
	if err != nil || inflight.Stop != "" || inflight.Deadline.After(time.Now()) {
		t.Fatalf("not truthfully degraded: %+v, %v", inflight, err)
	}
	var fireRun *agent.Run
	if blockedCancel {
		var ok bool
		fireRun, ok = svc.LookupRun(inflight.SessionID)
		if !ok {
			t.Fatal("fire run not registered")
		}
	}
	if _, err := lease.Acquire(ctx, inflight.SessionID, "competitor"); !errors.Is(err, port.ErrLeaseHeld) {
		t.Fatalf("fire ownership released at deadline: %v", err)
	}
	if _, err := s.FireNow(ctx, "uncooperative", time.Now()); !errors.Is(err, scheduler.ErrFireNowOverlap) {
		t.Fatalf("same-name fence released: %v", err)
	}
	select {
	case <-done:
		t.Fatal("fire returned with event producer still active")
	default:
	}
	if stopBeforeRecovery {
		stopped := make(chan struct{})
		go func() { defer close(stopped); _ = s.Stop() }()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("Stop exceeded its grace with uncooperative execution")
		}
		waitDeadlineDiagnostic(t, diag, "Stop grace elapsed")
		if _, err := lease.Acquire(ctx, inflight.SessionID, "competitor"); !errors.Is(err, port.ErrLeaseHeld) {
			t.Fatalf("Stop released execution ownership before producer closed: %v", err)
		}
		stillRunning, err := schedStore.LoadFire(ctx, inflight.ID)
		if err != nil || stillRunning.Stop != "" {
			t.Fatalf("Stop fabricated terminal completion: %+v %v", stillRunning, err)
		}
		select {
		case <-done:
			t.Fatal("Stop abandoned admitted execution")
		default:
		}
	}
	cancelCaller() // Cancel the scheduler's caller, not merely the run deadline.
	if blockedCancel {
		waitDeadlineGate(t, cancelEntered)
	}
	releaseOnce.Do(func() { close(llm.release) })
	if blockedCancel {
		if !eventually(5*time.Second, func() bool {
			snapshot, loadErr := store.Load(ctx, inflight.SessionID)
			return loadErr == nil && snapshot.State == session.StateCancelled
		}) {
			t.Fatal("event producer did not finish its terminal save")
		}
		// Wait until makeFireFunc has consumed the last event before observing
		// closure; the test must not steal the terminal result from its recorder.
		if !eventually(5*time.Second, func() bool {
			events, readErr := svc.StreamSessionEvents(ctx, inflight.SessionID)
			if readErr != nil {
				return false
			}
			for ev, eventErr := range events {
				if eventErr == nil && ev.Type == session.EvResult {
					return true
				}
			}
			return false
		}) {
			t.Fatal("fire did not record its terminal event")
		}
		select {
		case _, open := <-fireRun.Events():
			if open {
				t.Fatal("unexpected event after recorded terminal result")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("event producer did not close before cancellation release")
		}
		// Events are closed; only the watchdog control worker remains.
		select {
		case <-done:
			t.Fatal("real fire abandoned blocked cancellation after events closed")
		case <-time.After(50 * time.Millisecond):
		}
		if _, err := lease.Acquire(ctx, inflight.SessionID, "competitor"); !errors.Is(err, port.ErrLeaseHeld) {
			t.Fatalf("ownership released before cancellation joined: %v", err)
		}
		if _, err := s.FireNow(ctx, "uncooperative", time.Now()); !errors.Is(err, scheduler.ErrFireNowOverlap) {
			t.Fatalf("name fence released before cancellation joined: %v", err)
		}
		other := port.Schedule{Spec: port.ScheduleSpec{Name: "other", Trigger: port.TriggerSpec{Cron: "* * * * *"}}, State: port.ScheduleState{Enabled: true, NextFireAt: time.Now()}}
		if err := schedStore.Save(ctx, other); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FireNow(ctx, "other", time.Now()); err == nil || !strings.Contains(err.Error(), "capacity exhausted") {
			t.Fatalf("lifetime capacity released before cancellation joined: %v", err)
		}
		other, err = schedStore.Load(ctx, "other")
		if err != nil || other.State.FireCount != 0 {
			t.Fatalf("capacity-deferred claim changed: %+v %v", other.State, err)
		}
		cancelOnce.Do(func() { close(cancelRelease) })
	}
	waitDeadlineGate(t, done)
	if fireErr != nil || fire.Stop != session.StopTimeout {
		t.Fatalf("recovered terminal: %+v, %v", fire, fireErr)
	}
	terminal, err := schedStore.LoadFire(ctx, fire.ID)
	if err != nil || terminal.Stop != session.StopTimeout {
		t.Fatalf("durable timeout missing: %+v, %v", terminal, err)
	}
	if blockedCancel {
		pending, queueErr := queue.Pending(ctx, origin.ID)
		if queueErr != nil || len(pending) != 1 || !strings.Contains(pending[0].Text, fire.ID) {
			t.Fatalf("busy origin lost terminal notice: %+v %v", pending, queueErr)
		}
		originOnce.Do(func() { close(llm.originRelease) })
		drainDeliveryRun(svc, origin.ID, originRun)
		originRun = nil
		recovery, recoveryErr := svc.StartRunContent(ctx, origin.ID, "resume", nil)
		if recoveryErr != nil {
			t.Fatal(recoveryErr)
		}
		drainDeliveryRun(svc, origin.ID, recovery)
		pending, queueErr = queue.Pending(ctx, origin.ID)
		if queueErr != nil || len(pending) != 0 {
			t.Fatalf("terminal notice did not drain on recovery: %+v %v", pending, queueErr)
		}
	}
	after, err := svc.GetSession(ctx, origin.ID)
	if err != nil || !originHasNote(after, fire.ID) || !originHasNote(after, "timeout") {
		t.Fatalf("idle origin did not receive recovered timeout: %v", err)
	}
	svc.CloseSession(origin.ID)
	if n := svc.ActiveRuns(); n != 0 {
		t.Fatalf("resource ownership leaked: %d", n)
	}
	trial, err := lease.Acquire(ctx, fire.SessionID, "competitor")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(ctx, trial); err != nil {
		t.Fatal(err)
	}
	svc.Close()
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
}

type blockedAdmissionLease struct {
	port.SessionLease
	entered, release chan struct{}
	acquireCtx       context.Context
	once             sync.Once
}

func (l *blockedAdmissionLease) Acquire(ctx context.Context, id session.SessionID, owner string) (port.Lease, error) {
	l.once.Do(func() {
		l.acquireCtx = ctx
		close(l.entered)
		<-l.release // Deliberately uncooperative provisional admission.
	})
	if err := ctx.Err(); err != nil {
		return port.Lease{}, err
	}
	return l.SessionLease.Acquire(ctx, id, owner)
}

func TestFireProvisionalAdmissionDeadlineKeepsPendingUntilRecovery(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, ignore) })
	ctx := context.Background()
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	llm := &uncooperativeFireProvider{entered: make(chan struct{}), release: make(chan struct{})}
	diag := &deadlineDiagnostics{messages: make(chan string, 32)}
	leases := memlease.New(wallclock.Clock{}, 30*time.Second)
	lease := &blockedAdmissionLease{SessionLease: leases, entered: make(chan struct{}), release: make(chan struct{})}
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test", Store: store})
	svc, err := newTestServerService(server.Config{Engine: engine, Store: store, Now: time.Now, Diagnostics: diag, SessionLease: lease, LeaseOwner: "admission", LeaseTTL: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	schedStore := store.ScheduleStore()
	s := scheduler.New(scheduler.Config{Store: schedStore, Clock: wallclock.Clock{}, Fire: makeFireFunc(svc, schedStore, time.Second, nil), MaxConcurrentFires: 1})
	done := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() { close(lease.release) })
		close(llm.release)
		defer svc.Close()
		defer func() { _ = s.Stop() }()
		waitDeadlineGate(t, done)
	})
	now := time.Now()
	sched := port.Schedule{Spec: port.ScheduleSpec{Name: "pending-admission", Prompt: "wait", Trigger: port.TriggerSpec{Cron: "* * * * *"}, FireTimeout: time.Second, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"}, PlacementScope: "legacy-local"}, State: port.ScheduleState{Enabled: true, NextFireAt: now}}
	if err := schedStore.Save(ctx, sched); err != nil {
		t.Fatal(err)
	}
	var fire port.ScheduleFire
	var fireErr error
	go func() { defer close(done); fire, fireErr = s.FireNow(ctx, sched.Spec.Name, now) }()
	waitDeadlineGate(t, lease.entered)
	if deadline, ok := lease.acquireCtx.Deadline(); !ok || !deadline.Equal(now.Add(sched.Spec.FireTimeout)) {
		t.Fatalf("admission did not inherit the fire deadline: %v (present=%v)", deadline, ok)
	}
	waitDeadlineDiagnostic(t, diag, "termination unconfirmed")
	// The watchdog diagnostic and deadline cancellation are independent timers.
	waitDeadlineGate(t, lease.acquireCtx.Done())
	if !errors.Is(lease.acquireCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("admission context did not expire: %v", lease.acquireCtx.Err())
	}
	loaded, err := schedStore.Load(ctx, sched.Spec.Name)
	if err != nil || loaded.State.LastFireSessionID != port.PendingFireSessionID || loaded.State.LastFireAt.IsZero() || !loaded.State.LastFireStartedAt.IsZero() {
		t.Fatalf("provisional ownership published as execution: %+v %v", loaded.State, err)
	}
	fires, err := schedStore.ListFires(ctx, sched.Spec.Name)
	if err != nil || len(fires) != 0 {
		t.Fatalf("fabricated execution deadline/session before lease: %+v %v", fires, err)
	}
	if _, err := s.FireNow(ctx, sched.Spec.Name, time.Now()); !errors.Is(err, scheduler.ErrFireNowOverlap) {
		t.Fatalf("pending admission released name: %v", err)
	}
	select {
	case <-done:
		t.Fatal("admission abandoned at deadline")
	default:
	}
	once.Do(func() { close(lease.release) })
	waitDeadlineGate(t, done)
	if fireErr == nil || fire.Stop != session.StopError {
		t.Fatalf("admission recovery did not report actual failure: %+v %v", fire, fireErr)
	}
	terminal, err := schedStore.LoadFire(ctx, fire.ID)
	if err != nil || terminal.Stop != session.StopError || llm.calls.Load() != 0 || svc.ActiveRuns() != 0 {
		t.Fatalf("provisional admission failed to settle: %+v %v providerCalls=%d active=%d", terminal, err, llm.calls.Load(), svc.ActiveRuns())
	}
}
