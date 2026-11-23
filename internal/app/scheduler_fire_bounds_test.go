package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// stalledCall never answers on its own: a call ends when released or when its
// context does. It records whether it was called and with a deadline.
type stalledCall struct {
	release     chan struct{}
	called      atomic.Bool
	hadDeadline atomic.Bool
}

func newStalledCall() *stalledCall { return &stalledCall{release: make(chan struct{})} }

func (c *stalledCall) wait(ctx context.Context) error {
	_, ok := ctx.Deadline()
	c.hadDeadline.Store(ok)
	c.called.Store(true)
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func fireAsync(ctx context.Context, s *scheduler.Scheduler, name string) (<-chan struct{}, *port.ScheduleFire, *error) {
	done := make(chan struct{})
	var fire port.ScheduleFire
	var err error
	go func() { defer close(done); fire, err = s.FireNow(ctx, name, time.Now()) }()
	return done, &fire, &err
}

type stalledReserveProvider struct {
	*scheduledNativeProvider
	stall *stalledCall
}

func (p *stalledReserveProvider) ReserveSuccessor(ctx context.Context, req server.PlacementSuccessorRequest) (server.PlacementBinding, error) {
	if err := p.stall.wait(ctx); err != nil {
		return server.PlacementBinding{}, err
	}
	return p.scheduledNativeProvider.ReserveSuccessor(ctx, req)
}

// TestFirePlacementIsBoundedByFireDeadline pins that the fire deadline bounds
// placement (issue #2955): a remote placement that hangs fails the fire at its
// deadline instead of holding the claim, its name and its capacity.
func TestFirePlacementIsBoundedByFireDeadline(t *testing.T) {
	owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	stall := newStalledCall()
	provider := &stalledReserveProvider{stall: stall, scheduledNativeProvider: &scheduledNativeProvider{
		ref: session.EnvironmentRef{Kind: "kubernetes", ID: "env", Revision: "r1"}, owner: owner,
		published: map[session.SessionID]bool{}, pending: map[session.SessionID]bool{},
	}}
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llm := mockllm.New(mockllm.TextTurn("fire"))
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test"})
	lease := memlease.New(wallclock.Clock{}, 30*time.Second)
	svc, err := server.NewService(server.Config{
		Engine: engine, Store: store, PlacementProvider: provider, ExecutionAccess: provider,
		PlacementScope: "native-test", OwnershipEnforced: true, SessionLease: lease, LeaseOwner: "bounds", LeaseTTL: 30 * time.Second,
		SessionEngine: func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{Engine: engine, Close: func() error { return nil }}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.WithPrincipal(context.Background(), owner)
	origin, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatal(err)
	}
	svc.CloseSession(origin.ID)
	schedStore := store.ScheduleStore()
	s := scheduler.New(scheduler.Config{Store: schedStore, Lease: lease, LeaseOwner: "bounds-sched", Clock: wallclock.Clock{},
		Fire: makeFireFunc(svc, schedStore, time.Minute, nil), MaxConcurrentFires: 1})
	const name = "placement-stall"
	spec := port.ScheduleSpec{
		Name: name, Prompt: "fire", Mode: session.ModePlan, Owner: owner, Singleton: true,
		EnvironmentRef: origin.EnvironmentRef, PlacementScope: "native-test", FireTimeout: 300 * time.Millisecond,
		Selector: port.ScheduleProviderSelector{ProviderID: "test"}, OriginSessionID: origin.ID,
		Trigger: port.TriggerSpec{Cron: "* * * * *"},
	}
	if err := schedStore.Save(ctx, port.Schedule{Spec: spec, State: port.ScheduleState{Enabled: true, NextFireAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			close(stall.release)
		}
		_ = s.Stop()
		svc.Close()
	})

	done, fire, fireErr := fireAsync(ctx, s, name)
	waitDeadlineGate(t, done)
	if !stall.hadDeadline.Load() {
		t.Error("placement ran without the fire deadline")
	}
	const want = "scheduled fire wall-clock deadline of 300ms lapsed before the run started"
	if *fireErr == nil || (*fireErr).Error() != want || fire.Stop != session.StopError || fire.Err != want {
		t.Fatalf("fire = %+v, err = %v; want a failed fire with %q", *fire, *fireErr, want)
	}
	if st, err := schedStore.Load(ctx, name); err != nil || st.State.LastFireSessionID == port.PendingFireSessionID {
		t.Fatalf("claim still pending after the fire failed: %+v %v", st.State, err)
	}

	// The claim, name and only capacity slot are free again: once placement
	// answers, the next fire runs.
	released = true
	close(stall.release)
	spec.FireTimeout = 30 * time.Second
	if err := schedStore.Save(ctx, port.Schedule{Spec: spec}); err != nil {
		t.Fatal(err)
	}
	done, fire, fireErr = fireAsync(ctx, s, name)
	waitDeadlineGate(t, done)
	if *fireErr != nil || fire.Stop != session.StopEndTurn || llm.Calls() != 1 {
		t.Fatalf("second fire = %+v, err = %v, model calls = %d", *fire, *fireErr, llm.Calls())
	}
}

// stalledCreateStore never answers Create for a fire session made under a
// deadline, like a remote session store that hangs.
type stalledCreateStore struct {
	*jsonlstore.Store
	stall *stalledCall
}

func (s stalledCreateStore) Create(ctx context.Context, sess *session.Session) error {
	if _, ok := ctx.Deadline(); ok && strings.HasPrefix(string(sess.ID), "sched--") {
		if err := s.stall.wait(ctx); err != nil {
			return err
		}
	}
	return s.Store.Create(ctx, sess)
}

// TestFireSessionCreationIsBoundedByFireDeadline pins that the fire deadline
// bounds the fire session's creation (issue #2955).
func TestFireSessionCreationIsBoundedByFireDeadline(t *testing.T) {
	ctx := context.Background()
	inner, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stall := newStalledCall()
	store := stalledCreateStore{Store: inner, stall: stall}
	llm := mockllm.New(mockllm.TextTurn("fire"))
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test", Store: store})
	svc, err := newTestServerService(server.Config{Engine: engine, Store: store, Now: time.Now, EventLog: store})
	if err != nil {
		t.Fatal(err)
	}
	schedStore := inner.ScheduleStore()
	s := scheduler.New(scheduler.Config{Store: schedStore, Clock: wallclock.Clock{},
		Fire: makeFireFunc(svc, schedStore, time.Minute, nil), MaxConcurrentFires: 1})
	t.Cleanup(func() {
		close(stall.release)
		_ = s.Stop()
		svc.Close()
	})
	const name = "create-stall"
	if err := schedStore.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{
		Name: name, Prompt: "fire", FireTimeout: 300 * time.Millisecond, Trigger: port.TriggerSpec{Cron: "* * * * *"},
		EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: t.TempDir(), Revision: "in-tree-v1"}, PlacementScope: "legacy-local",
	}, State: port.ScheduleState{Enabled: true, NextFireAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}

	done, fire, fireErr := fireAsync(ctx, s, name)
	waitDeadlineGate(t, done)
	if !stall.called.Load() {
		t.Fatal("the fire session was not created under the fire deadline")
	}
	const want = "scheduled fire wall-clock deadline of 300ms lapsed before the run started"
	if *fireErr == nil || (*fireErr).Error() != want || fire.Stop != session.StopError {
		t.Fatalf("fire = %+v, err = %v; want a failed fire with %q", *fire, *fireErr, want)
	}
	if llm.Calls() != 0 {
		t.Errorf("model calls = %d, want 0: the run must not start", llm.Calls())
	}
}

type stalledRecordStore struct {
	port.ScheduleStore
	start, progress *stalledCall
}

func (s *stalledRecordStore) RecordFireStart(ctx context.Context, name string, f port.ScheduleFire) error {
	if s.start != nil {
		if err := s.start.wait(ctx); err != nil {
			return err
		}
	}
	return s.ScheduleStore.RecordFireStart(ctx, name, f)
}

func (s *stalledRecordStore) RecordFireProgress(ctx context.Context, name, fireID string, at time.Time) error {
	if s.progress != nil {
		if err := s.progress.wait(ctx); err != nil {
			return err
		}
	}
	return s.ScheduleStore.RecordFireProgress(ctx, name, fireID, at)
}

// runStalledRecordFires fires twice against a schedule store that never
// answers one in-flight record write, with that write's bound shortened and a
// fire deadline far beyond the wait, so only the write bound can free the fire.
func runStalledRecordFires(t *testing.T, store *stalledRecordStore, stall *stalledCall, warn string) {
	t.Helper()
	prev := fireRecordTimeout
	fireRecordTimeout = 200 * time.Millisecond
	t.Cleanup(func() { fireRecordTimeout = prev })

	ctx := context.Background()
	sessions, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.ScheduleStore = sessions.ScheduleStore()
	llm := mockllm.New(mockllm.TextTurn("fire one"), mockllm.TextTurn("fire two"))
	diag := &deadlineDiagnostics{messages: make(chan string, 64)}
	lease := memlease.New(wallclock.Clock{}, 30*time.Second)
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test", Store: sessions})
	svc, err := newTestServerService(server.Config{Engine: engine, Store: sessions, Now: time.Now, EventLog: sessions, Diagnostics: diag,
		SessionLease: lease, LeaseOwner: "bounds", LeaseTTL: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	s := scheduler.New(scheduler.Config{Store: store, Lease: lease, LeaseOwner: "bounds-sched", Clock: wallclock.Clock{},
		Fire: makeFireFunc(svc, store, time.Minute, nil), MaxConcurrentFires: 1, Diagnostics: diag})
	t.Cleanup(func() {
		close(stall.release)
		_ = s.Stop()
		svc.Close()
	})
	const name = "record-stall"
	if err := store.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{
		Name: name, Prompt: "fire", Singleton: true, Trigger: port.TriggerSpec{Cron: "* * * * *"},
		EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: t.TempDir(), Revision: "in-tree-v1"}, PlacementScope: "legacy-local",
	}, State: port.ScheduleState{Enabled: true, NextFireAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}

	// The name, lease and only capacity slot are released each time.
	for i, want := range []int{1, 2} {
		done, fire, fireErr := fireAsync(ctx, s, name)
		waitDeadlineGate(t, done)
		if *fireErr != nil || fire.Stop != session.StopEndTurn || llm.Calls() != want {
			t.Fatalf("fire %d = %+v, err = %v, model calls = %d", i+1, *fire, *fireErr, llm.Calls())
		}
	}
	if !stall.called.Load() || !stall.hadDeadline.Load() {
		t.Errorf("stalled write called=%v with deadline=%v, want a bounded call", stall.called.Load(), stall.hadDeadline.Load())
	}
	waitDeadlineDiagnostic(t, diag, warn)
}

// TestFireRecordStartIsBounded pins that a schedule store that never answers
// RecordFireStart cannot hold the fire (issue #2955): the write times out and
// the fire still runs to completion.
func TestFireRecordStartIsBounded(t *testing.T) {
	stall := newStalledCall()
	runStalledRecordFires(t, &stalledRecordStore{start: stall}, stall, "RecordFireStart failed")
}

// TestFireRecordProgressIsBounded pins the same bound for RecordFireProgress.
func TestFireRecordProgressIsBounded(t *testing.T) {
	stall := newStalledCall()
	runStalledRecordFires(t, &stalledRecordStore{progress: stall}, stall, "RecordFireProgress failed")
}
