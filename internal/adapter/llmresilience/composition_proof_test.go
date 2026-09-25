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
	t.Run("attempt observer", func(t *testing.T) { testBlockedRecoveryCallback(t, false) })
	t.Run("health diagnostics", func(t *testing.T) { testBlockedRecoveryCallback(t, true) })
}

func testBlockedRecoveryCallback(t *testing.T, diagnostics bool) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	ctx := t.Context()
	if !diagnostics {
		ctx = port.WithAttemptObserver(ctx, func(session.NetworkAttemptPayload) {
			close(entered)
			<-release
		})
	}
	waiting := make(chan struct{}, 1)
	cfg := recoveryConfig(1, 5*time.Second)
	cfg.BreakerThreshold = 1
	cfg.BreakerCooldown = 2 * time.Second
	cfg.Diagnostics = recoveryWaitSignal{entered: waiting}
	if diagnostics {
		cfg.Diagnostics = blockedRecoveryDiagnostics{recoveryWaitSignal: recoveryWaitSignal{entered: waiting}, entered: entered, release: release}
	}
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
	defer cancel()
	waitDone := make(chan error, 1)
	go func() {
		_, err := provider.Stream(waitCtx, port.LLMRequest{})
		waitDone <- err
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		close(release)
		cancel()
		t.Fatal("second caller never reached breaker admission wait")
	}
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

type blockedRecoveryDiagnostics struct {
	recoveryWaitSignal
	entered chan struct{}
	release <-chan struct{}
}

func (d blockedRecoveryDiagnostics) Log(ctx context.Context, level port.Level, msg string, args ...any) {
	if msg == "llm circuit breaker opened" {
		close(d.entered)
		<-d.release
	}
	d.recoveryWaitSignal.Log(ctx, level, msg, args...)
}

func TestRecoveryCancellationAfterAdmission(t *testing.T) {
	for _, phase := range []string{"provider call", "breaker wait"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered := make(chan struct{}, 1)
			cfg := recoveryConfig(2, time.Minute)
			cfg.BreakerThreshold = 1
			cfg.BreakerCooldown = 2 * time.Second
			cfg.Diagnostics = recoveryWaitSignal{entered: entered}
			inner := &fakeProvider{steps: []step{{block: true}}, onAttempt: func(context.Context, int) { entered <- struct{}{} }}
			provider := Wrap(inner, cfg).(*resilientProvider)
			if phase == "breaker wait" {
				provider.recordOutcome(0, false, breakerFailure)
			}
			done := make(chan error, 1)
			go func() {
				_, err := recoveryDrain(t, provider, ctx)
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("recovery never entered requested phase")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("active operation did not cancel")
			}
			want := 1
			if phase == "breaker wait" {
				want = 0
			}
			if inner.Calls() != want {
				t.Fatalf("actual calls=%d want %d", inner.Calls(), want)
			}
		})
	}
}

type recoveryWaitSignal struct {
	port.NopDiagnostics
	entered chan struct{}
}

func (d recoveryWaitSignal) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	if msg == "llm provider recovery" && argValue(args, "decision") == "wait" {
		d.entered <- struct{}{}
	}
}
