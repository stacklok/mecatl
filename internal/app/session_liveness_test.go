package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionLivenessConcurrentRegistrationsReleaseWithoutLeak(t *testing.T) {
	const workers = 64
	const id = session.SessionID("subagent-shared")
	registry := newSessionLiveness(nil, "", 0, 0, nil)
	registered := make(chan struct{}, workers)
	releaseAll := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			release, err := registry.Register(context.Background(), id, func() {})
			if err != nil {
				t.Errorf("Register() error = %v", err)
				return
			}
			registered <- struct{}{}
			<-releaseAll
			release()
			release()
		}()
	}
	for range workers {
		<-registered
	}
	if !registry.IsLive(id) {
		t.Fatal("concurrent registrations did not keep the session live")
	}
	close(releaseAll)
	wg.Wait()
	if registry.IsLive(id) {
		t.Fatal("concurrent terminal releases leaked a registry entry")
	}
}

func TestSessionLivenessDistributedExclusionAcrossBuildOwners(t *testing.T) {
	backend := memlease.New(wallclock.Clock{}, time.Minute)
	first := newSessionLiveness(backend, "replica-a", time.Minute, time.Millisecond, nil)
	second := newSessionLiveness(backend, "replica-b", time.Minute, time.Millisecond, nil)
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)

	release, err := first.Register(context.Background(), "team-live-lead", func() {})
	if err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	if _, err := second.Register(context.Background(), "team-live-lead", func() {}); !errors.Is(err, port.ErrLeaseHeld) {
		t.Fatalf("second Register() error = %v, want ErrLeaseHeld", err)
	}
	release()

	secondRelease, err := second.Register(context.Background(), "team-live-lead", func() {})
	if err != nil {
		t.Fatalf("second Register() after release error = %v", err)
	}
	secondRelease()
}

type failingChildLease struct{ err error }

func (l failingChildLease) Acquire(context.Context, session.SessionID, string) (port.Lease, error) {
	return port.Lease{}, l.err
}
func (failingChildLease) Renew(context.Context, port.Lease) (port.Lease, error) {
	panic("Renew called after failed Acquire")
}
func (failingChildLease) Release(context.Context, port.Lease) error {
	panic("Release called after failed Acquire")
}

func TestSessionLivenessAcquireFailureDoesNotRegisterOrRenew(t *testing.T) {
	want := errors.New("lease backend unavailable")
	registry := newSessionLiveness(failingChildLease{err: want}, "replica", time.Minute, time.Millisecond, nil)
	defer registry.Close()

	if _, err := registry.Register(context.Background(), "subagent-queued", func() {}); !errors.Is(err, want) {
		t.Fatalf("Register() error = %v, want wrapped acquisition error", err)
	}
	if registry.IsLive("subagent-queued") {
		t.Fatal("failed acquisition left child locally live")
	}
}
