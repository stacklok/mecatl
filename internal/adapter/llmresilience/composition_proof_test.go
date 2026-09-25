package llmresilience

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestServerProviderRecovery_Scenario7_BlockedObserverDoesNotHoldAdmissionMutex(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	ctx := port.WithAttemptObserver(t.Context(), func(session.NetworkAttemptPayload) {
		close(entered)
		<-release
	})
	cfg := recoveryConfig(1, time.Second)
	cfg.BreakerThreshold = 1
	provider := Wrap(&fakeProvider{steps: []step{{outerErr: apiErr(503)}}}, cfg).(*resilientProvider)
	done := make(chan error, 1)
	go func() {
		_, err := provider.Stream(ctx, port.LLMRequest{})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("attempt observer was not called")
	}

	mutexFree := make(chan struct{})
	go func() {
		provider.breaker.mu.Lock()
		provider.breaker.mu.Unlock()
		close(mutexFree)
	}()
	select {
	case <-mutexFree:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("blocked observer held breaker admission mutex")
	}

	waitCtx, cancel := context.WithCancel(t.Context())
	waitDone := make(chan error, 1)
	go func() {
		_, err := provider.Stream(waitCtx, port.LLMRequest{})
		waitDone <- err
	}()
	cancel()
	select {
	case err := <-waitDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled waiter error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked observer held breaker admission mutex")
	}
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("failed attempt unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked attempt did not finish after observer release")
	}
}

func TestServerProviderRecovery_Scenario6_DisconnectAndShutdownCancelRecovery(t *testing.T) {
	for _, phase := range []string{"provider call", "breaker wait"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cfg := recoveryConfig(2, time.Minute)
			cfg.BreakerThreshold = 1
			provider := Wrap(&fakeProvider{steps: []step{{outerErr: apiErr(503)}, {block: true}}}, cfg).(*resilientProvider)
			if phase == "breaker wait" {
				provider.recordOutcome(0, false, breakerFailure)
				provider.cfg.BreakerCooldown = time.Hour
			}
			done := make(chan error, 1)
			go func() {
				_, err := recoveryDrain(t, provider, ctx)
				done <- err
			}()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("recovery continued after caller disconnect/shutdown")
			}
			provider.breaker.mu.Lock()
			halfOpen := provider.breaker.halfOpen
			provider.breaker.mu.Unlock()
			if halfOpen {
				t.Fatal("cancellation retained probe ownership")
			}
		})
	}
}
