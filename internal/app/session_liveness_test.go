package app

import (
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionLivenessConcurrentRegistrationsReleaseWithoutLeak(t *testing.T) {
	const workers = 64
	const id = session.SessionID("subagent-shared")
	registry := newSessionLiveness()
	registered := make(chan struct{}, workers)
	releaseAll := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			release := registry.Register(id)
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
