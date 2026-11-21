package sessiongroup_test

import (
	"context"
	"errors"
	"testing"

	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
	"github.com/stacklok/mecatl/engine/kernel/sessiongroup"
	groupid "github.com/stacklok/mecatl/engine/kernel/sessiongroup/id"
)

func TestSkeletonCannotEstablishHosting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, canceled := range []bool{false, true} {
		if canceled {
			cancel()
		}
		provider := &testProvider{}
		clock := &testClock{}
		g, err := sessiongroup.Open(ctx, groupid.NewGroupID(), validConfig(provider, clock))
		if g != nil || !errors.Is(err, sessiongroup.ErrNotImplemented) {
			t.Fatalf("Open created a placeholder owner: %v, %v", g, err)
		}
		if provider.called || clock.nowCalled || clock.waitCalled {
			t.Fatal("skeleton Open executed an injected dependency")
		}
		config := validConfig(provider, clock)
		config.OpeningTimeout = 0 // Invalid inputs must not activate dependencies either.
		g, err = sessiongroup.Open(ctx, groupid.NewGroupID(), config)
		if g != nil || !errors.Is(err, sessiongroup.ErrNotImplemented) ||
			provider.called || clock.nowCalled || clock.waitCalled {
			t.Fatalf("Open activated or validated invalid config: %v, %v", g, err)
		}
		g, err = sessiongroup.Open(ctx, groupid.GroupID{}, sessiongroup.Config{})
		if g != nil || !errors.Is(err, sessiongroup.ErrNotImplemented) {
			t.Fatalf("Open validated configuration instead of remaining a stub: %v, %v", g, err)
		}
		for _, g := range []*sessiongroup.SessionGroup{nil, {}} {
			if g.GroupID() != (groupid.GroupID{}) {
				t.Fatal("unconstructed group has identity")
			}
			members, err := g.Members(ctx)
			if members != nil || !errors.Is(err, sessiongroup.ErrNotImplemented) {
				t.Fatalf("Members claimed membership: %v, %v", members, err)
			}
			loaded, err := g.LoadedRuntimes()
			if loaded != nil || !errors.Is(err, sessiongroup.ErrNotImplemented) {
				t.Fatalf("LoadedRuntimes claimed residency: %v, %v", loaded, err)
			}
			member := sessionid.NewSessionID()
			runtime, err := g.Load(ctx, member)
			if runtime != nil || !errors.Is(err, sessiongroup.ErrNotImplemented) {
				t.Fatalf("Load created a usable runtime: %v, %v", runtime, err)
			}
			if err := g.Unload(ctx, member); !errors.Is(err, sessiongroup.ErrNotImplemented) {
				t.Fatalf("Unload claimed success: %v", err)
			}
			for range 2 {
				if err := g.Drain(); !errors.Is(err, sessiongroup.ErrNotImplemented) {
					t.Fatalf("Drain claimed shutdown initiation: %v", err)
				}
				status, err := g.Status()
				if status != (sessiongroup.LifecycleStatus{}) || !errors.Is(err, sessiongroup.ErrNotImplemented) {
					t.Fatalf("Status claimed lifecycle/ownership: %+v, %v", status, err)
				}
				if g.Done() != nil {
					t.Fatal("unconstructed group exposes a completion signal")
				}
				report, err := g.Report()
				if report.GroupID != (groupid.GroupID{}) || report.Release != sessiongroup.ShutdownReleaseUnknown ||
					report.Unresolved != nil || !errors.Is(err, sessiongroup.ErrNotImplemented) {
					t.Fatalf("Report claimed final accounting: %+v, %v", report, err)
				}
			}
		}
	}
}
