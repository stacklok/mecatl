package app

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/port"
)

type discoveryUnauthorized struct{}

func (discoveryUnauthorized) Error() string   { return "unauthorized fixture" }
func (discoveryUnauthorized) StatusCode() int { return 401 }

type discoveryListerFunc func(context.Context) ([]modelEntry, error)

func (f discoveryListerFunc) ListModels(ctx context.Context) ([]modelEntry, error) { return f(ctx) }

// checkedWaitContext puts cancellation between request's initial context check
// and its attempt join, without scheduling sleeps or reaching into a mutex.
type checkedWaitContext struct {
	context.Context
	checked, proceed chan struct{}
	once             sync.Once
}

func (c *checkedWaitContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked); <-c.proceed })
	return err
}

func testCancelledJoinDuringPublication(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	bEntered, bRelease := make(chan struct{}), make(chan struct{})
	reg := &providerRegistry{defaultID: providerToolhive, entries: map[string]providerEntry{
		providerToolhive: {id: providerToolhive, available: true, intentDriven: true,
			lister: &fakeLister{models: []modelEntry{{ID: "m", ContextLimit: 222222}}},
			remint: func(string, port.ProviderCapabilities) port.LLMProvider { close(entered); <-release; return nil },
		},
		"b": {id: "b", available: true, lister: discoveryListerFunc(func(context.Context) ([]modelEntry, error) {
			close(bEntered)
			<-bRelease
			return []modelEntry{{ID: "b", ContextLimit: 333333}}, nil
		})},
	}}
	d := newProviderDiscovery(reg, Config{})
	defer d.Close()
	defer closeIfOpen(release)
	defer closeIfOpen(bRelease)
	primary := make(chan error, 1)
	go func() {
		_, err := d.request(context.Background(), providerToolhive, discoveryPicker)
		primary <- err
	}()
	select {
	case <-entered:
	case err := <-primary:
		t.Fatalf("request completed without entering candidate barrier: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("candidate did not reach remint barrier")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiter := &checkedWaitContext{Context: ctx, checked: make(chan struct{}), proceed: make(chan struct{})}
	defer closeIfOpen(waiter.proceed)
	joined := make(chan error, 1)
	go func() { _, err := d.request(waiter, providerToolhive, discoveryPicker); joined <- err }()
	select {
	case <-waiter.checked:
	case <-time.After(5 * time.Second):
		t.Fatal("join did not check caller cancellation")
	}
	cancel()
	close(waiter.proceed)
	select {
	case err := <-joined:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled join = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled join waited behind another caller's publication tail")
	}
	bDone := make(chan error, 1)
	go func() { _, err := d.request(context.Background(), "b", discoveryAdmission); bDone <- err }()
	select {
	case <-bEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("new provider waited behind another provider's local tail")
	}
	before := d.snapshot().providers["b"]
	close(release)
	if err := <-primary; err != nil {
		t.Fatal(err)
	}
	if !before.inFlight || before.attemptID != 1 || !reflect.DeepEqual(before, d.snapshot().providers["b"]) {
		t.Fatal("completion overwrote an unrelated provider's newly started attempt")
	}
	close(bRelease)
	if err := <-bDone; err != nil {
		t.Fatal(err)
	}
}

func discoveryFixture(t *testing.T, entries map[string]providerEntry) *providerDiscovery {
	t.Helper()
	for pid, entry := range entries {
		entry.id, entry.available = pid, true
		entries[pid] = entry
	}
	reg := &providerRegistry{entries: entries}
	d := newProviderDiscovery(reg, Config{})
	reg.discovery = d
	reg.meta = &liveMetaStore{owner: d}
	t.Cleanup(d.Close)
	return d
}

func TestProviderModelDiscovery_Scenario2_TimeoutAndLateReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		d := discoveryFixture(t, map[string]providerEntry{"a": {lister: discoveryListerFunc(func(context.Context) ([]modelEntry, error) {
			n := calls.Add(1)
			if n == 1 {
				<-release
			}
			return []modelEntry{{ID: "target", ContextLimit: int(n) * 200000}}, nil
		})}})
		defer closeIfOpen(release)
		done := make(chan struct{})
		go func() { _, _ = d.request(context.Background(), "a", discoveryAdmission); close(done) }()
		synctest.Wait()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("owner timeout did not wake waiter")
		}
		first := d.snapshot().providers["a"]
		if first.outcome.State != statusUnreachable || first.inFlight || !first.completedAt.Equal(time.Now()) {
			t.Fatalf("timeout publication: %+v", first)
		}
		time.Sleep(11 * time.Second)
		_, _ = d.request(context.Background(), "a", discoveryPicker)
		if calls.Load() != 1 {
			t.Fatal("overlapping fetch after timeout")
		}
		close(release)
		synctest.Wait()
		if !reflect.DeepEqual(first, d.snapshot().providers["a"]) {
			t.Fatal("late return changed publication/cooldown")
		}
		_, err := d.request(context.Background(), "a", discoveryAdmission)
		if err != nil {
			t.Fatal(err)
		}
		if got := resolveModelWindow(Config{}, d.snapshot(), "a", "target"); got.tokens != 400000 || got.source != windowLive {
			t.Fatalf("next demand: %+v", got)
		}
	})
}

func closeIfOpen(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func TestProviderModelDiscovery_Scenario4_WaiterCancellation(t *testing.T) {
	t.Run("cancel join during publication", testCancelledJoinDuringPublication)
	t.Run("all waiters leave", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			release := make(chan struct{})
			defer closeIfOpen(release)
			var calls atomic.Int32
			d := discoveryFixture(t, map[string]providerEntry{"a": {lister: discoveryListerFunc(func(context.Context) ([]modelEntry, error) {
				calls.Add(1)
				<-release
				return []modelEntry{{ID: "m", ContextLimit: 222222}}, nil
			})}})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 2)
			for range 2 {
				go func() { _, err := d.request(ctx, "a", discoveryPicker); done <- err }()
			}
			synctest.Wait()
			cancel()
			for range 2 {
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled waiter=%v", err)
				}
			}
			close(release)
			synctest.Wait()
			if calls.Load() != 1 || resolveModelWindow(Config{}, d.snapshot(), "a", "m").tokens != 222222 {
				t.Fatal("all-waiter cancellation prevented successful publication")
			}
		})
	})
	t.Run("late join keeps original deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int32
			d := discoveryFixture(t, map[string]providerEntry{"a": {lister: discoveryListerFunc(func(ctx context.Context) ([]modelEntry, error) {
				calls.Add(1)
				<-ctx.Done()
				return nil, ctx.Err()
			})}})
			started := time.Now()
			ctx, cancel := context.WithCancel(context.Background())
			first := make(chan error, 1)
			go func() { _, err := d.request(ctx, "a", discoveryPicker); first <- err }()
			synctest.Wait()
			cancel()
			if err := <-first; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			time.Sleep(9 * time.Second)
			joined := make(chan error, 1)
			go func() { _, err := d.request(context.Background(), "a", discoveryAdmission); joined <- err }()
			synctest.Wait()
			select {
			case err := <-joined:
				t.Fatalf("late join returned before original deadline: %v", err)
			default:
			}
			time.Sleep(time.Second)
			if err := <-joined; err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || !d.snapshot().providers["a"].completedAt.Equal(started.Add(10*time.Second)) {
				t.Fatal("late join extended/replaced original attempt")
			}
		})
	})
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		d := discoveryFixture(t, map[string]providerEntry{"a": {lister: discoveryListerFunc(func(ctx context.Context) ([]modelEntry, error) {
			calls.Add(1)
			select {
			case <-release:
				return []modelEntry{{ID: "m", ContextLimit: 200000}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})}})
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { _, err := d.request(ctx, "a", discoveryPicker); result <- err }()
		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error: %v", err)
		}
		if !d.snapshot().providers["a"].inFlight {
			t.Fatal("waiter cancelled shared fetch")
		}
		time.Sleep(5 * time.Second)
		joined := make(chan error, 1)
		go func() { _, err := d.request(context.Background(), "a", discoveryAdmission); joined <- err }()
		synctest.Wait()
		close(release)
		if err := <-joined; err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 1 || d.snapshot().providers["a"].outcome.State != statusOK {
			t.Fatal("join did not share successful publication")
		}
	})
}

func TestProviderModelDiscovery_Scenario1_ProviderIsolation(t *testing.T) {
	t.Run("independent deadline and cooldown", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			lister := discoveryListerFunc(func(ctx context.Context) ([]modelEntry, error) { <-ctx.Done(); return nil, ctx.Err() })
			d := discoveryFixture(t, map[string]providerEntry{"a": {lister: lister}, "b": {lister: lister}})
			start := time.Now()
			aDone, bDone := make(chan struct{}), make(chan struct{})
			go func() { _, _ = d.request(context.Background(), "a", discoveryAdmission); close(aDone) }()
			synctest.Wait()
			time.Sleep(9 * time.Second)
			go func() { _, _ = d.request(context.Background(), "b", discoveryAdmission); close(bDone) }()
			synctest.Wait()
			time.Sleep(time.Second)
			<-aDone
			select {
			case <-bDone:
				t.Fatal("A consumed B's independent attempt budget")
			default:
			}
			time.Sleep(9 * time.Second)
			<-bDone
			view := d.snapshot()
			if !view.providers["a"].nextEligible.Equal(start.Add(20*time.Second)) || !view.providers["b"].nextEligible.Equal(start.Add(29*time.Second)) {
				t.Fatalf("provider cooldowns are not independent: %+v", view.providers)
			}
		})
	})
	synctest.Test(t, func(t *testing.T) {
		a := make(chan struct{})
		b := make(chan struct{})
		var bCalls atomic.Int32
		d := discoveryFixture(t, map[string]providerEntry{
			"a": {lister: discoveryListerFunc(func(ctx context.Context) ([]modelEntry, error) {
				select {
				case <-a:
					return nil, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})},
			"b": {lister: discoveryListerFunc(func(context.Context) ([]modelEntry, error) {
				bCalls.Add(1)
				<-b
				return []modelEntry{{ID: "m", ContextLimit: 200000}}, nil
			})},
		})
		defer closeIfOpen(a)
		defer closeIfOpen(b)
		picker := make(chan struct{})
		go func() { d.refresh(context.Background(), discoveryPicker); close(picker) }()
		synctest.Wait()
		joined := make(chan struct{})
		go func() { _, _ = d.request(context.Background(), "b", discoveryAdmission); close(joined) }()
		synctest.Wait()
		close(b)
		<-joined
		if bCalls.Load() != 1 || resolveModelWindow(Config{}, d.snapshot(), "b", "m").tokens != 200000 {
			t.Fatal("B did not join/publish independently")
		}
		select {
		case <-picker:
			t.Fatal("held A unexpectedly completed")
		default:
		}
		close(a)
		<-picker
	})
}

func TestProviderModelDiscovery_Scenario2_LastGoodAndLatestOutcome(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		models := []modelEntry{{ID: "kept", ContextLimit: 222222}, {ID: "changed", ContextLimit: 111111}}
		var failure error
		d := discoveryFixture(t, map[string]providerEntry{"a": {lister: discoveryListerFunc(func(context.Context) ([]modelEntry, error) { return models, failure })}})
		_, _ = d.request(context.Background(), "a", discoveryPicker)
		synctest.Wait()
		observed := d.snapshot().providers["a"].observedAt
		for _, tc := range []struct {
			err   error
			state string
		}{
			{errors.New("offline"), statusUnreachable}, {discoveryUnauthorized{}, statusUnauthorized}, {nil, statusEmpty},
		} {
			time.Sleep(discoveryCooldown)
			failure, models = tc.err, nil
			_, _ = d.request(context.Background(), "a", discoveryPicker)
			synctest.Wait()
			view := d.snapshot()
			if got := view.providers["a"].outcome.State; got != tc.state {
				t.Fatalf("latest outcome=%s, want %s", got, tc.state)
			}
			if got := resolveModelWindow(Config{}, view, "a", "kept"); got.tokens != 222222 || got.source != windowLive {
				t.Fatalf("lost positive last-good: %+v", got)
			}
			if !view.providers["a"].observedAt.Equal(observed) {
				t.Fatal("failed/empty refresh renewed observation age")
			}
			if got := resolveModelWindow(Config{}, view, "a", "unknown"); got.admissible || got.tokens != 0 {
				t.Fatalf("stale omission evidence: %+v", got)
			}
		}
		time.Sleep(discoveryCooldown)
		models = []modelEntry{{ID: "new", ContextLimit: 333333}, {ID: "changed", ContextLimit: 444444}}
		_, _ = d.request(context.Background(), "a", discoveryPicker)
		if got := resolveModelWindow(Config{}, d.snapshot(), "a", "changed"); got.tokens != 444444 || got.source != windowLive {
			t.Fatalf("new success did not replace changed window: %+v", got)
		}
		if _, ok := d.snapshot().lookup("a", "kept"); ok {
			t.Fatal("new success retained omitted observation")
		}
		if got := resolveModelWindow(Config{}, d.snapshot(), "a", "kept"); got.source != windowFallback || got.tokens != 128000 {
			t.Fatalf("healthy omission: %+v", got)
		}
	})
}

func TestProviderModelDiscovery_Scenario2_ProjectionIsolation(t *testing.T) {
	models := []modelEntry{{ID: "m", ContextLimit: 222222, InputModalities: []string{"text"}}}
	d := discoveryFixture(t, map[string]providerEntry{"a": {defaultModel: "m", lister: discoveryListerFunc(func(context.Context) ([]modelEntry, error) { return models, nil })}})
	_, _ = d.request(context.Background(), "a", discoveryPicker)
	models[0].ContextLimit = 1
	models[0].InputModalities[0] = "image"
	projection := d.CurrentModelSnapshot()
	projection.Models[0].ContextLimit = 2
	projection.ProviderStatus[0].State = "corrupt"
	entry, _ := d.snapshot().lookup("a", "m")
	if entry.ContextLimit != 222222 || entry.InputModalities[0] != "text" {
		t.Fatal("lister aliases accepted state")
	}
	again := d.CurrentModelSnapshot()
	if again.Models[0].ContextLimit != 222222 || again.ProviderStatus[0].State != statusOK {
		t.Fatal("public projection aliases accepted state")
	}
}
