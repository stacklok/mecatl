package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
	mcpsource "github.com/stacklok/mecatl/internal/adapter/mcp/source"
)

func TestMCPReconcilerNotificationDuringPublication(t *testing.T) {
	var notify func()
	builds := 0
	r := &mcpSourceReconciler{
		ctx: context.Background(), trigger: make(chan struct{}, 1), lkg: make(map[string]mcpSourceSnapshot),
		build: func(_ context.Context, configs []mcp.ServerConfig, changed func()) (*mcpReconcileCandidate, error) {
			builds++
			notify = changed
			return candidateFromConfigs(configs, 0), nil
		},
		publish: func(_, _ *mcpReconcileCandidate) bool {
			notify() // The manager is live before the reconciler installs current.
			return true
		},
	}
	if _, err := r.cycle(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.cycle(); err != nil {
		t.Fatal(err)
	}
	if builds != 2 {
		t.Fatalf("publication-time notification lost: builds=%d, want 2", builds)
	}
}

func TestMCPReconcilerDirtyRetryAfterFailedCandidate(t *testing.T) {
	for _, failure := range []string{"build", "validation", "publication"} {
		t.Run(failure, func(t *testing.T) {
			var notify func()
			builds := 0
			r := &mcpSourceReconciler{
				ctx: context.Background(), trigger: make(chan struct{}, 1), lkg: make(map[string]mcpSourceSnapshot),
				build: func(_ context.Context, configs []mcp.ServerConfig, changed func()) (*mcpReconcileCandidate, error) {
					builds++
					notify = changed
					if builds == 2 && failure == "build" {
						return nil, errors.New("offline failure")
					}
					c := candidateFromConfigs(configs, 0)
					c.tools = []mcpToolMeta{{Name: "same-name", Description: "old"}}
					if builds > 1 {
						c.tools[0].Description = "changed"
					}
					if builds == 2 && failure == "validation" {
						c.bytes = maxMCPCandidateBytes + 1
					}
					return c, nil
				},
				publish: func(_, _ *mcpReconcileCandidate) bool { return builds != 2 || failure != "publication" },
			}
			initial, err := r.cycle()
			if err != nil {
				t.Fatal(err)
			}
			notify()
			failed, _ := r.cycle()
			if !failed.stale || r.current != initial.candidate {
				t.Fatal("failed attempt did not retain stale current runtime")
			}
			// A later poll/manual request or retirement drain must retry even
			// though the source configs did not change and no new event arrived.
			retried, err := r.cycle()
			if err != nil || !retried.changed || builds != 3 || retried.candidate.tools[0].Description != "changed" {
				t.Fatalf("dirty retry lost: builds=%d result=%+v err=%v", builds, retried, err)
			}
		})
	}
}

func TestMCPReconcilerNotificationStormCooldown(t *testing.T) {
	type timer struct {
		delay time.Duration
		tick  chan time.Time
	}
	timers := make(chan timer, 4)
	var builds atomic.Int64
	r := newMCPSourceReconciler(mcpReconcilerOptions{
		sources: []mcpsource.Source{&reconciliationSource{name: "static"}},
		after: func(d time.Duration) <-chan time.Time {
			tick := make(chan time.Time, 1)
			timers <- timer{d, tick}
			return tick
		},
		build: func(_ context.Context, configs []mcp.ServerConfig, changed func()) (*mcpReconcileCandidate, error) {
			builds.Add(1)
			for range 1000 {
				changed()
			}
			return candidateFromConfigs(configs, 0), nil
		},
	})
	t.Cleanup(r.Close)
	if _, err := r.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	for want := int64(1); want <= 2; want++ {
		var timer timer
		select {
		case timer = <-timers:
		case <-time.After(time.Second):
			t.Fatal("notification successor did not arm a cooldown")
		}
		if timer.delay != minMCPReconcileCooldown {
			t.Fatalf("cooldown = %v, want exactly %v", timer.delay, minMCPReconcileCooldown)
		}
		select {
		case <-timers:
			t.Fatal("successor ran before the cooldown expired")
		case <-time.After(20 * time.Millisecond):
		}
		if got := builds.Load(); got != want {
			t.Fatalf("storm escaped cooldown: builds=%d, want %d", got, want)
		}
		if want == 1 {
			timer.tick <- time.Now()
		}
	}
	// Shutdown must join even with a dirty successor waiting on an unexpired timer.
	done := make(chan struct{})
	go func() { r.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked on cooldown")
	}
}
