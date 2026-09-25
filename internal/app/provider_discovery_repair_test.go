package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestProviderModelDiscovery_Scenario4_AdmissionCallerCancellation(t *testing.T) {
	testAdmissionWaitIsolation(t, true)
}

func TestProviderModelDiscovery_Scenario3_AdmissionInternalWaitExpiry(t *testing.T) {
	testAdmissionWaitIsolation(t, false)
}

func testAdmissionWaitIsolation(t *testing.T, callerCancel bool) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		defer closeIfOpen(release)
		var calls atomic.Int32
		var fetchCtx context.Context
		d := discoveryFixture(t, map[string]providerEntry{"p": {lister: discoveryListerFunc(func(ctx context.Context) ([]modelEntry, error) {
			calls.Add(1)
			fetchCtx = ctx
			<-release
			return []modelEntry{{ID: "m", ContextLimit: 222222}}, nil
		})}})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- awaitContextWindowWithin(ctx, d.reg, "p", "m", time.Second) }()
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("wrapper did not enter the shared lister")
		}
		select {
		case err := <-done:
			t.Fatalf("wrapper returned before caller cancellation or admission deadline: %v", err)
		default:
		}
		if callerCancel {
			cancel()
		} else {
			time.Sleep(time.Second)
		}
		err := <-done
		if callerCancel {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("caller cancellation = %v, want context.Canceled", err)
			}
		} else if !errors.Is(err, server.ErrContextWindowUnavailable) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("internal admission expiry = %v, want ErrContextWindowUnavailable without DeadlineExceeded", err)
		}
		if err := fetchCtx.Err(); err != nil {
			t.Fatalf("waiter cancelled shared fetch: %v", err)
		}
		close(release)
		synctest.Wait()
		if calls.Load() != 1 || d.snapshot().providers["p"].outcome.State != statusOK {
			t.Fatal("shared fetch did not publish after waiter left")
		}
		if err := awaitContextWindowWithin(context.Background(), d.reg, "p", "m", time.Second); err != nil {
			t.Fatalf("later admission did not reuse successful shared fetch: %v", err)
		}
	})
}

func TestProviderDiscoveryBuildListModelsRefreshesHealthyProvider(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		cfg := nativeRegistryConfig("native", &nativeBearerFixture{token: "fixture"}, nativeRoundTripFunc(func(*http.Request) (*http.Response, error) {
			n := calls.Add(1)
			body := `{"data":[{"id":"native-model","context_window":222222}]}`
			if n > 1 {
				body = `{"data":[{"id":"native-model","context_window":333333}]}`
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}))
		cfg.MockProvider = nil
		cfg.Workspace = t.TempDir()
		cfg.NoSoul, cfg.NoShell, cfg.NoUserModel = true, true, true
		cfg.envDetector = fakeEnv(nil)
		built, err := buildIsolated(t, context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer built.Close()
		if calls.Load() != 0 {
			t.Fatal("native Build performed eager discovery")
		}
		first := built.Service.ListModels(context.Background())
		synctest.Wait()
		if calls.Load() != 1 || len(first) != 1 || first[0].ContextLimit != 222222 {
			t.Fatalf("initial listing: calls=%d models=%v", calls.Load(), first)
		}
		time.Sleep(discoveryCooldown - time.Nanosecond)
		before := built.Service.ListModels(context.Background())
		if calls.Load() != 1 || len(before) != 1 || before[0].ContextLimit != 222222 {
			t.Fatalf("before cooldown: calls=%d models=%v", calls.Load(), before)
		}
		time.Sleep(time.Nanosecond)
		after := built.Service.ListModels(context.Background())
		if calls.Load() != 2 || len(after) != 1 || after[0].ContextLimit != 333333 {
			t.Fatalf("healthy refresh: calls=%d models=%v", calls.Load(), after)
		}
		if first[0].ContextLimit != 222222 {
			t.Fatal("refresh mutated an earlier returned snapshot")
		}
	})
}

func TestProviderDiscoveryCloseDuringStartupDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lister := &fakeLister{models: []modelEntry{{ID: "m", ContextLimit: 222222}}}
		d := discoveryFixture(t, map[string]providerEntry{"p": {lister: lister}})
		before := d.snapshot()
		d.start(false, time.Hour)
		synctest.Wait() // the owned startup worker is parked on its retained delay
		started := time.Now()
		done := make(chan struct{})
		go func() { d.Close(); close(done) }()
		<-done
		if elapsed := time.Since(started); elapsed != 0 {
			t.Fatalf("Close waited for startup delay instead of cancelling it: %v", elapsed)
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if lister.calls.Load() != 0 || d.snapshot() != before {
			t.Fatalf("Close during startup delay listed or published: calls=%d", lister.calls.Load())
		}
	})
}
