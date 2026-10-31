package app

import (
	"context"
	"errors"
	"sync"
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

// TestIssue2588_SingletonHungFirePastDeadlineAllowsNextTick reproduces the
// production failure mode from https://github.com/stacklok/mecatl/issues/2588
// at the composition layer: a singleton schedule fires once, the LLM stream
// blocks past the per-fire deadline while the session lease stays held, and a
// later scheduler tick must not stall forever — the hung fire is reaped and the
// next due slot fires.
func TestIssue2588_SingletonHungFirePastDeadlineAllowsNextTick(t *testing.T) {
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
	lease := memlease.New(wallclock.Clock{}, 30*time.Second)
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test",
		Store:   store,
	})
	svc, err := newTestServerService(server.Config{
		Engine:       engine,
		Store:        store,
		Now:          time.Now,
		EventLog:     store,
		Diagnostics:  diag,
		SessionLease: lease,
		LeaseOwner:   "issue-2588",
		LeaseTTL:     30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	schedStore := store.ScheduleStore()
	const schedName = "singleton-hung-2588"
	fireTimeout := 100 * time.Millisecond
	now := time.Now()

	fireFunc := makeFireFunc(svc, schedStore, fireTimeout, nil)
	s := scheduler.New(scheduler.Config{
		Store:              schedStore,
		Lease:              lease,
		LeaseOwner:         "issue-2588-sched",
		Clock:              wallclock.Clock{},
		Fire:               fireFunc,
		MaxConcurrentFires: 2,
		Diagnostics:        diag,
		ReconcileStaleFire: makeReconcileStaleFire(svc, schedStore, store),
	})

	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:           schedName,
			Prompt:         "block past deadline",
			Trigger:        port.TriggerSpec{Cron: "* * * * *"},
			Singleton:      true,
			FireTimeout:    fireTimeout,
			EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"},
			PlacementScope: "legacy-local",
		},
		State: port.ScheduleState{Enabled: true, NextFireAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var first port.ScheduleFire
	var firstErr error
	go func() {
		defer close(done)
		first, firstErr = s.FireNow(ctx, schedName, now)
	}()

	waitDeadlineGate(t, llm.entered)
	waitDeadlineDiagnostic(t, diag, "termination unconfirmed")

	loaded, err := schedStore.Load(ctx, schedName)
	if err != nil {
		t.Fatal(err)
	}
	inflight, err := schedStore.LoadFire(ctx, string(loaded.State.LastFireSessionID))
	if err != nil {
		t.Fatal(err)
	}
	if inflight.Stop != "" {
		t.Fatalf("fire already terminal before recovery: %+v", inflight)
	}
	if inflight.Deadline.After(time.Now()) {
		t.Fatalf("deadline not yet lapsed: %v", inflight.Deadline)
	}
	if _, err := lease.Acquire(ctx, inflight.SessionID, "competitor"); !errors.Is(err, port.ErrLeaseHeld) {
		t.Fatalf("lease released while hung: %v", err)
	}

	// Second due tick while the first fire is still hung past deadline.
	nextDue := time.Now()
	sched, err := schedStore.Load(ctx, schedName)
	if err != nil {
		t.Fatal(err)
	}
	sched.State.NextFireAt = nextDue
	if err := schedStore.Save(ctx, sched); err != nil {
		t.Fatal(err)
	}
	s.RunOnceForTest(ctx)

	fires, err := schedStore.ListFires(ctx, schedName)
	if err != nil {
		t.Fatal(err)
	}
	terminalCount := 0
	for _, f := range fires {
		if f.Stop != "" {
			terminalCount++
		}
	}
	if terminalCount == 0 {
		t.Fatal("no terminal fire after tick past deadline (singleton schedule still stalled; issue #2588)")
	}

	after, err := schedStore.Load(ctx, schedName)
	if err != nil {
		t.Fatal(err)
	}
	if !after.State.LastFireStartedAt.IsZero() {
		t.Errorf("LastFireStartedAt still set after reaping: %v", after.State.LastFireStartedAt)
	}

	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(llm.release) })
		waitDeadlineGate(t, done)
		_ = s.Stop()
		svc.Close()
	})
	releaseOnce.Do(func() { close(llm.release) })
	waitDeadlineGate(t, done)
	if firstErr != nil || first.Stop != session.StopTimeout {
		t.Fatalf("first fire recovery: %+v, %v", first, firstErr)
	}
}
