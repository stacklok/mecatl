package app_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// observe records the context a port actually saw on the named path, from
// whatever goroutine the production wiring ran it on. The path label is what
// lets a root with SEVERAL boundary-crossing paths (the scheduler: tick, fire,
// delivery, reconcile) be asserted on EACH of them rather than on whichever one
// happens to report first.
type observe func(ctx context.Context, path string)

// starter drives ONE registered internal goroutine root through its REAL
// production entry point, wired to port probes that hand back the context the
// goroutine crossed each boundary with. paths enumerates every path the root
// MUST be observed on; the harness waits for all of them and asserts each.
type starter struct {
	paths []string
	run   func(ctx context.Context, t *testing.T, seen observe)
}

// TestCallerSeparation_Scenario4_InternalWorkersUseOnlyClassifiedAccess pins
// AC4.2 through the registered-root conformance driver below. It exercises the
// real child GC and both consolidators at their port boundary; the registry
// equality assertion prevents a new unclassified worker from escaping the set.
func TestCallerSeparation_Scenario4_InternalWorkersUseOnlyClassifiedAccess(t *testing.T) {
	TestCallerIdentity_Scenario2_InternalGoroutinesRunAsSystem(t)
}

// TestCallerIdentity_Scenario2_InternalGoroutinesRunAsSystem pins AC2.2: every
// internal goroutine root runs under an EXPLICIT system principal, never an
// absent one (ADR 0204 decision 7).
//
// The set is enumerated ONCE — syscaller.Roots is the registry, and this table
// must cover it exactly. A goroutine that registers a root but forgets the wrap
// at its call site fails its subtest (its port observes a nil principal); a root
// registered with no driver here fails the coverage assertion.
func TestCallerIdentity_Scenario2_InternalGoroutinesRunAsSystem(t *testing.T) {
	t.Parallel()

	starters := map[syscaller.Root]starter{
		syscaller.RootChildGC: {paths: []string{"store.List"}, run: func(ctx context.Context, _ *testing.T, seen observe) {
			// The sweeper's first act is a store List — the port boundary.
			app.StartChildGCForTest(ctx, app.Config{ChildRetention: time.Hour},
				&probeSessionStore{Store: memstore.New(), seen: seen},
				func(session.SessionID) bool { return false })
		}},
		syscaller.RootMemoryConsolidation: {paths: []string{"memory.List"}, run: func(ctx context.Context, _ *testing.T, seen observe) {
			app.StartMemoryConsolidationForTest(ctx,
				app.Config{MemoryConsolidateInterval: time.Millisecond},
				probeMemoryStore{seen: seen}, nil)
		}},
		syscaller.RootUserModelConsolidation: {paths: []string{"memory.List"}, run: func(ctx context.Context, _ *testing.T, seen observe) {
			app.StartUserModelConsolidationForTest(ctx,
				app.Config{UserModelConsolidateInterval: time.Millisecond},
				probeMemoryStore{seen: seen}, mockllm.New(mockllm.TextTurn("unused")))
		}},
		// The scheduler root fans out into FOUR boundary-crossing paths, all
		// descending from Start's one syscaller wrap. Every one is asserted: the
		// tick's Due poll alone would leave fire, delivery and reconcile resting
		// on an inheritance argument, so a stray context.Background() in any of
		// them would pass.
		syscaller.RootScheduler: {
			paths: []string{"store.Due", "fire", "delivery", "reconcile"},
			run:   startProbedScheduler,
		},
		syscaller.RootModelCatalogRefresh: {paths: []string{"lister.ListModels"}, run: func(_ context.Context, _ *testing.T, seen observe) {
			app.ObserveStartupModelRefreshForTest(func(ctx context.Context) {
				seen(ctx, "lister.ListModels")
			})
		}},
		syscaller.RootJWKSRefresh: {paths: []string{"validator.New"}, run: func(ctx context.Context, t *testing.T, seen observe) {
			// The validator owns background key rotation, so the ctx it is
			// CONSTRUCTED with is the refresh goroutine's root.
			_, err := cliconfig.OIDCValidator(ctx, cliconfig.OIDCConfig{
				Issuer:   "https://idp.example",
				Audience: "mecatl",
				NewValidator: func(ctx context.Context, _ cliconfig.OIDCConfig) (server.PrincipalValidator, error) {
					seen(ctx, "validator.New")
					return probeValidator{}, nil
				},
			})
			if err != nil {
				t.Fatalf("OIDCValidator: %v", err)
			}
		}},
	}

	// The registry and the table are ONE set: a new root with no driver here is
	// red, and a driver for an unregistered root is red.
	if len(starters) != len(syscaller.Roots) {
		t.Fatalf("registry/table drift: %d registered roots, %d drivers", len(syscaller.Roots), len(starters))
	}
	for _, root := range syscaller.Roots {
		if _, ok := starters[root]; !ok {
			t.Fatalf("registered root %q has no driver in this test", root)
		}
	}

	for _, root := range syscaller.Roots {
		t.Run(string(root), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// The root context carries NO principal: whatever the port sees is
			// what the production wiring stamped, nothing inherited from here.
			st := starters[root]
			rec := &pathRecorder{want: st.paths, got: map[string]context.Context{}, done: make(chan struct{})}
			st.run(ctx, t, rec.observe)
			select {
			case <-rec.done:
			case <-time.After(10 * time.Second):
				t.Fatalf("%s: never crossed these port boundaries: %v", root, rec.missing())
			}
			for _, path := range st.paths {
				p := session.PrincipalFromContext(rec.contextFor(path))
				if p == nil {
					t.Fatalf("%s (%s): port observed an ABSENT principal; that path's context is not wrapped with the system principal", root, path)
				}
				if p.GrantType != session.GrantTypeSystem {
					t.Errorf("%s (%s): grant type = %q, want %q", root, path, p.GrantType, session.GrantTypeSystem)
				}
				if p.Subject != string(root) {
					t.Errorf("%s (%s): subject = %q, want %q (the root must stamp its OWN identity)", root, path, p.Subject, root)
				}
			}
		})
	}
}

// pathRecorder collects the FIRST context observed on each declared path and
// closes done once every one has reported. Observations arrive from the
// production goroutines, so it is mutex-guarded.
type pathRecorder struct {
	mu     sync.Mutex
	want   []string
	got    map[string]context.Context
	done   chan struct{}
	closed bool
}

func (r *pathRecorder) observe(ctx context.Context, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, seen := r.got[path]; !seen {
		r.got[path] = ctx
	}
	if r.closed || len(r.got) < len(r.want) {
		return
	}
	for _, p := range r.want {
		if _, seen := r.got[p]; !seen {
			return
		}
	}
	r.closed = true
	close(r.done)
}

func (r *pathRecorder) contextFor(path string) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got[path]
}

func (r *pathRecorder) missing() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, p := range r.want {
		if _, seen := r.got[p]; !seen {
			out = append(out, p)
		}
	}
	return out
}

// startProbedScheduler drives the REAL scheduler through Start (the one place
// the syscaller wrap happens) with its store pre-seeded so all four paths fire:
// a DUE cron schedule drives the fire path and, through RecordFire, the delivery
// callback; a stale claimed-but-never-started fire drives the reconcile scan;
// and the tick's own Due poll is observed by the store probe.
func startProbedScheduler(ctx context.Context, t *testing.T, seen observe) {
	store := memschedulestore.New()
	now := time.Now()
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:    "due-cron",
			Prompt:  "x",
			Trigger: port.TriggerSpec{Cron: "* * * * *"},
		},
		State: port.ScheduleState{NextFireAt: now.Add(-time.Second), Enabled: true},
	}); err != nil {
		t.Fatalf("Save(due-cron): %v", err)
	}
	// Claimed (NextFireAt already advanced) but never started, and old enough to
	// be past the reconciler's stale window.
	if err := store.Save(context.Background(), port.Schedule{
		Spec: port.ScheduleSpec{
			Name:    "crashed-claim",
			Prompt:  "x",
			Trigger: port.TriggerSpec{Cron: "* * * * *"},
		},
		State: port.ScheduleState{
			NextFireAt:        now.Add(time.Hour),
			Enabled:           true,
			LastFireAt:        now.Add(-2 * time.Hour),
			LastFireSessionID: port.PendingFireSessionID,
		},
	}); err != nil {
		t.Fatalf("Save(crashed-claim): %v", err)
	}

	s := scheduler.New(scheduler.Config{
		Store:        &probeScheduleStore{Store: store, seen: seen},
		Clock:        wallclock.Clock{},
		TickInterval: time.Millisecond,
	})
	s.SetFire(func(fctx context.Context, _ port.Schedule, _ time.Time) (port.ScheduleFire, error) {
		seen(fctx, "fire")
		return port.ScheduleFire{ID: "fire-1", SessionID: "sched--due-cron", Stop: "end_turn"}, nil
	})
	s.SetDeliverFireResult(func(dctx context.Context, _ port.Schedule, _ port.ScheduleFire) {
		seen(dctx, "delivery")
	})
	s.SetReconcileStaleFire(func(rctx context.Context, _ port.Schedule) {
		seen(rctx, "reconcile")
	})
	if err := s.Start(ctx); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
}

// --- port probes: real reference adapters, wrapped to report the ctx ---------

type probeSessionStore struct {
	*memstore.Store
	seen observe
}

func (p *probeSessionStore) List(ctx context.Context) ([]port.StoredSession, error) {
	p.seen(ctx, "store.List")
	return p.Store.List(ctx)
}

func (p *probeSessionStore) PageSessionMetadata(ctx context.Context, request port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	p.seen(ctx, "store.List")
	return p.Store.PageSessionMetadata(ctx, request)
}

type probeScheduleStore struct {
	*memschedulestore.Store
	seen observe
}

func (p *probeScheduleStore) Due(ctx context.Context, now time.Time) ([]port.Schedule, error) {
	p.seen(ctx, "store.Due")
	return p.Store.Due(ctx, now)
}

// probeMemoryStore reports the ctx of the consolidator's first port call. The
// consolidator's List is its entry point and a 0-entry store is a no-op run, so
// no other method is ever reached.
type probeMemoryStore struct {
	tool.MemoryStore
	seen observe
}

func (p probeMemoryStore) List(ctx context.Context, _ string) ([]tool.MemoryEntry, error) {
	p.seen(ctx, "memory.List")
	return nil, nil
}

type probeValidator struct{}

func (probeValidator) Validate(context.Context, string) (*session.Principal, error) {
	return nil, server.ErrInvalidToken
}
