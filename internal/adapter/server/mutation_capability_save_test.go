package server

import (
	"runtime"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestMutationCapabilityInvalidateWaitsForAdmittedMutation(t *testing.T) {
	id := session.SessionID("mutation")
	capability := NewSessionMutationCapability(true)
	capability.Grant(id)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	mutated := make(chan error, 1)
	go func() {
		_, err := capability.withMutation(id, func() error {
			entered <- struct{}{}
			<-release
			return nil
		})
		mutated <- err
	}()
	<-entered

	invalidated := make(chan struct{})
	invalidateStarted := make(chan struct{})
	go func() {
		close(invalidateStarted)
		capability.Invalidate(id)
		close(invalidated)
	}()
	<-invalidateStarted
	deadline := time.Now().Add(time.Second)
	for capability.mu.TryRLock() {
		capability.mu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("invalidation did not begin waiting for the admitted mutation")
		}
		runtime.Gosched()
	}
	select {
	case <-invalidated:
		t.Fatal("invalidation overtook an admitted in-memory mutation")
	default:
	}
	close(release)
	if err := <-mutated; err != nil {
		t.Fatal(err)
	}
	select {
	case <-invalidated:
	case <-time.After(time.Second):
		t.Fatal("invalidation did not proceed after in-memory mutation completed")
	}
}
